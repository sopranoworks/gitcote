package main

import (
	"fmt"
	"log/slog"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/pr"
)

// reconcileOrphanedAgents runs once at process startup and closes a restart
// gap in spawnAgentForPR/spawnAgentForSeedSync (cmd/gitcote/eventwiring.go):
// both write an AgentWorkdirRecord with Status "running" *before* executing
// the agent process, and only ever move it to "completed"/"failed"/"killed"
// from inside the same goroutine that ran it. If gitcote's process is
// killed or crashes while that goroutine is still running, nothing updates
// the record — it stays "running" forever, because the only code that would
// ever revisit it (reconcileExternalMerges/reconcileExternalSeedSync) fires
// solely off a future git push, and cleanupTempClones explicitly skips
// "running" records on its periodic sweep. The stale AgentTokenRecord issued
// alongside it is just as durable and just as stuck.
//
// Left unreconciled, this is a genuine dead end for the operator: PRs with
// no prior InterruptInfo (StateOpen/StateMergeConflict, never-attempted
// path) are refused by prRetryEligible's "already has an agent running"
// check (cmd/gitcote/prwiring.go) forever, and the seed-sync recovery bar
// keeps showing a "running" agent that will never finish.
//
// Because this only runs once, at the very start of a fresh process, before
// any agent goroutine has had a chance to register itself, any workdir
// record still marked "running" at this point is unambiguously orphaned —
// the process that could ever transition it away from "running" no longer
// exists. So every such record is treated as failed: the associated PR (or
// seed sync) is transitioned to an interrupted state with reason
// "server_restarted", the stale agent token is revoked so Retry is no
// longer blocked, and the workdir record itself is moved off "running" so
// the periodic temp-clone cleanup can reap it.
func reconcileOrphanedAgents(ec *eventContext, logger *slog.Logger) {
	if ec == nil || ec.integrityHS == nil {
		return
	}
	workdirs, err := ec.integrityHS.ListAgentWorkdirs()
	if err != nil {
		logger.Error("reconcile orphaned agents: list workdirs", "error", err)
		return
	}
	for _, rec := range workdirs {
		if rec.Status != "running" {
			continue
		}
		logger.Warn("reconcile orphaned agents: found agent still marked running at startup, treating as orphaned",
			"namespace", rec.Namespace, "project", rec.Project, "pr", rec.PRNumber,
			"agent", rec.AgentName, "role", rec.Role, "path", rec.Path)

		detail := fmt.Sprintf("gitcote restarted while %s agent %q was executing; its process state after restart is unknown", rec.Role, rec.AgentName)

		if rec.PRNumber != 0 {
			reconcileOrphanedPRAgent(ec, rec, detail, logger)
		} else {
			reconcileOrphanedSeedSyncAgent(ec, rec, detail, logger)
		}

		if uerr := ec.integrityHS.UpdateAgentWorkdir(rec.Path, "orphaned", -1); uerr != nil {
			logger.Warn("reconcile orphaned agents: update workdir status", "path", rec.Path, "error", uerr)
		}
	}
}

// reconcileOrphanedPRAgent marks the PR the orphaned agent was running
// against as interrupted (unless it already reached a terminal state before
// the crash) and revokes the stale agent token blocking prRetryEligible.
func reconcileOrphanedPRAgent(ec *eventContext, rec integrity.AgentWorkdirRecord, detail string, logger *slog.Logger) {
	prStore, serr := getPRStore(ec.gitStore.BaseDir(), rec.Namespace, rec.Project)
	if serr != nil {
		logger.Warn("reconcile orphaned agents: open PR store", "namespace", rec.Namespace, "project", rec.Project, "error", serr)
	} else if p, gerr := prStore.Get(uint32(rec.PRNumber)); gerr == nil && p != nil {
		switch p.State {
		case pr.StateMerged, pr.StateRejected, pr.StateClosed:
			// Already reached a terminal state (e.g. resolved externally and
			// reconciled on a prior restart/push) — leave it alone.
		default:
			markInterrupted(prStore, p, "server_restarted", detail, rec.AgentName, rec.Role, logger)
		}
	} else if gerr != nil {
		logger.Warn("reconcile orphaned agents: load PR", "namespace", rec.Namespace, "project", rec.Project, "pr", rec.PRNumber, "error", gerr)
	}
	ensureNoActiveToken(ec, rec.Namespace, rec.Project, rec.PRNumber)
}

// reconcileOrphanedSeedSyncAgent marks the project's seed sync as interrupted
// (preserving whatever direction was already recorded) and revokes the stale
// agent token.
func reconcileOrphanedSeedSyncAgent(ec *eventContext, rec integrity.AgentWorkdirRecord, detail string, logger *slog.Logger) {
	direction := ""
	if projPath, perr := ec.gitStore.ProjectPath(rec.Namespace, rec.Project); perr == nil {
		if cfg, cerr := git.LoadSeedConfig(projPath); cerr == nil && cfg != nil && cfg.SyncStatus != nil {
			direction = cfg.SyncStatus.Direction
		}
	}
	updateSeedSyncStateDetail(ec.gitStore, rec.Namespace, rec.Project, "interrupted", direction, "server_restarted", detail)
	ensureNoActiveToken(ec, rec.Namespace, rec.Project, 0)
}
