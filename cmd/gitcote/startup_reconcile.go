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

	reconcileOrphanedTokens(ec, logger)
}

// reconcileOrphanedTokens closes the "spawn-starting" gap: issueAgentToken
// durably writes an AgentTokenRecord *before* AddAgentWorkdir is ever
// called — agent.PrepareWorkDir/WriteMCPConfig disk I/O sits in between
// (cmd/gitcote/eventwiring.go, executeAgentForPR/executeAgentForSeedSync). A
// crash in that window leaves a token with no matching "running" workdir
// record at all — invisible to the sweep above, which only iterates workdir
// records — so it would otherwise block prRetryEligible's "already has an
// agent running" check forever with no diagnostic trail. Must run AFTER the
// workdir sweep above: that sweep's ensureNoActiveToken calls already
// remove every token tied to a "running" record, so whatever remains here
// is, by construction, exactly the set with no matching record.
func reconcileOrphanedTokens(ec *eventContext, logger *slog.Logger) {
	tokens, err := ec.integrityHS.ListAgentTokens()
	if err != nil {
		logger.Error("reconcile orphaned agents: list tokens", "error", err)
		return
	}
	for _, tok := range tokens {
		logger.Warn("reconcile orphaned agents: found agent token with no matching workdir record at startup, treating as orphaned",
			"namespace", tok.Namespace, "project", tok.Project, "pr", tok.PRNumber,
			"agent", tok.AgentName, "role", tok.Role)

		detail := fmt.Sprintf("gitcote restarted while %s agent %q was being spawned; it may never have started, or its process state is unknown", tok.Role, tok.AgentName)

		if tok.PRNumber != 0 {
			prStore, serr := getPRStore(ec.gitStore.BaseDir(), tok.Namespace, tok.Project)
			if serr == nil {
				if p, gerr := prStore.Get(uint32(tok.PRNumber)); gerr == nil && p != nil {
					switch p.State {
					case pr.StateMerged, pr.StateRejected, pr.StateClosed, pr.StateInterrupted:
						// Terminal, or already interrupted by something else
						// (e.g. the workdir sweep above) — leave PR state
						// alone, just clear the stale token below.
					default:
						markInterrupted(prStore, p, "server_restarted", detail, tok.AgentName, tok.Role, logger)
					}
				}
			}
		} else {
			direction := ""
			if projPath, perr := ec.gitStore.ProjectPath(tok.Namespace, tok.Project); perr == nil {
				if cfg, cerr := git.LoadSeedConfig(projPath); cerr == nil && cfg != nil && cfg.SyncStatus != nil {
					direction = cfg.SyncStatus.Direction
				}
			}
			updateSeedSyncStateDetail(ec.gitStore, tok.Namespace, tok.Project, "interrupted", direction, "server_restarted", detail)
		}

		ensureNoActiveToken(ec, tok.Namespace, tok.Project, tok.PRNumber)
	}
}

// reconcileExternalStateAtStartup runs once at startup, after
// reconcileOrphanedAgents, and closes restart gaps that fall outside the
// agent-process bookkeeping model entirely: cases where durable PR/seed-sync
// state can lag behind what actually happened in git, or where a project's
// PR queue can fall out of sync with the PRs it's supposed to track. Unlike
// reconcileOrphanedAgents, these aren't about an orphaned process — they're
// about a multi-write sequence (a git ref update, then a separate bbolt
// write recording it) that a crash can interrupt partway through, with no
// AgentWorkdirRecord anywhere to signal it.
//
// It reuses the existing reconcileExternalMerges/reconcileExternalSeedSync
// machinery — previously wired only to fire reactively on a future git push,
// inside handlePostReceive — rather than inventing a new comparison: the
// git-ancestry check those functions already do ("has this branch already
// been merged in git, regardless of what our own bookkeeping says") is
// exactly what's needed here too; it just also needs to run once up front
// instead of waiting on unrelated future push traffic.
func reconcileExternalStateAtStartup(gitStore *git.Store, ec *eventContext, logger *slog.Logger) {
	if ec == nil || ec.integrityHS == nil {
		return
	}
	projects, err := gitStore.ListProjects("")
	if err != nil {
		logger.Error("reconcile external state at startup: list projects", "error", err)
		return
	}
	for _, proj := range projects {
		reconcileQueueMembership(ec, proj.Namespace, proj.Project, logger)
		reconcileIdleSeedSyncSlot(ec, proj.Namespace, proj.Project, logger)
		reconcileExternalMerges(gitStore, ec, proj.Namespace, proj.Project, logger)
		reconcileExternalSeedSync(gitStore, ec, proj.Namespace, proj.Project, logger)
	}
}

// reconcileQueueMembership closes the "PR created but never enqueued" gap:
// create_pull_request/handlePostReceive write the PR row (prStore.Create)
// and enqueue it (integrityHS.EnqueuePR) as two separate writes to two
// separate bbolt databases — not atomic with each other. A crash between
// them leaves a durable PR with no queue entry at all: no reviewer is ever
// spawned, and — since the PR row already exists — even re-pushing the same
// branch pair is refused ("PR already exists"), so there is no natural
// retry path either. EnqueuePriority is idempotent (no-ops if the PR is
// already active or waiting), so it's safe to call unconditionally for
// every non-terminal PR as a pure consistency check; its return value is
// deliberately ignored here — this must never re-dispatch an agent, only
// repair queue membership.
func reconcileQueueMembership(ec *eventContext, ns, proj string, logger *slog.Logger) {
	prStore, err := getPRStore(ec.gitStore.BaseDir(), ns, proj)
	if err != nil {
		return
	}
	prs, err := prStore.List("")
	if err != nil {
		return
	}
	for i := range prs {
		p := &prs[i]
		switch p.State {
		case pr.StateMerged, pr.StateRejected, pr.StateClosed:
			continue
		}
		if _, qerr := ec.integrityHS.EnqueuePriority(ns, proj, int(p.Number)); qerr != nil {
			logger.Warn("reconcile queue membership: enqueue", "namespace", ns, "project", proj, "pr", p.Number, "error", qerr)
		}
	}
}

// reconcileIdleSeedSyncSlot closes the narrower mirror of the same problem
// for seed sync: updateSeedSyncState(idle) and releaseSeedSyncSlot are two
// separate bbolt writes (in verifySeedSyncAfterAgent and
// reconcileExternalSeedSync/reconcileExternalPushSync alike). A crash
// between them leaves SyncStatus already correctly "idle" while the
// project's seed-sync queue slot is still held — permanently, since
// reconcileExternalSeedSync's own state gate only proceeds for
// "interrupted"/"conflict"/"error", never "idle", so nothing else will ever
// revisit it. Safe ONLY as a startup-only check: unlike the gates in
// reconcileExternalMerges/reconcileExternalSeedSync (which independently
// verify against git ref state before acting), "idle" alone doesn't prove
// anything mid-run — a live, in-flight successful pull can transiently read
// SyncStatus as "idle" (unchanged from before it started) while genuinely,
// correctly still holding the slot a few instructions before releasing it
// itself. At startup there is no such live goroutine to race against, so
// the check is unambiguous here.
func reconcileIdleSeedSyncSlot(ec *eventContext, ns, proj string, logger *slog.Logger) {
	q, err := ec.integrityHS.GetPRQueue(ns, proj)
	if err != nil || q.ActivePR != integrity.SeedSyncSentinel {
		return
	}
	projPath, err := ec.gitStore.ProjectPath(ns, proj)
	if err != nil {
		return
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil || cfg.SyncStatus == nil || cfg.SyncStatus.State != "idle" {
		return
	}
	logger.Warn("reconcile idle seed sync slot: seed sync already idle but its queue slot was never released, releasing it now",
		"namespace", ns, "project", proj)
	releaseSeedSyncSlot(ec, ns, proj)
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
