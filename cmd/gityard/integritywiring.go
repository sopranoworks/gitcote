package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/integrity"
	"github.com/sopranoworks/shoka/pkg/uiws"
)

const MsgRepoIntegrityAlert uiws.MessageType = "REPO_INTEGRITY_ALERT"

// headStore is the package-level integrity store, initialized in run().
var headStore *integrity.Store

// IntegrityAlert represents a detected external modification.
type IntegrityAlert struct {
	Namespace    string `json:"namespace"`
	Project      string `json:"project"`
	ExpectedHead string `json:"expected_head"`
	ActualHead   string `json:"actual_head"`
	DetectedAt   string `json:"detected_at"`
}

// IntegrityStatus holds aggregate stats for the Server Info page.
type IntegrityStatus struct {
	mu            sync.RWMutex
	LastCheckAt   time.Time
	ReposChecked  int
	MismatchCount int64
	RecentAlerts  []IntegrityAlert
}

func (s *IntegrityStatus) record(checked int, alerts []IntegrityAlert) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastCheckAt = time.Now()
	s.ReposChecked = checked
	s.MismatchCount += int64(len(alerts))
	s.RecentAlerts = append(s.RecentAlerts, alerts...)
	if len(s.RecentAlerts) > 20 {
		s.RecentAlerts = s.RecentAlerts[len(s.RecentAlerts)-20:]
	}
}

func (s *IntegrityStatus) snapshot() (lastCheck time.Time, reposChecked int, mismatchCount int64, alerts []IntegrityAlert) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]IntegrityAlert, len(s.RecentAlerts))
	copy(cp, s.RecentAlerts)
	return s.LastCheckAt, s.ReposChecked, s.MismatchCount, cp
}

func startIntegrityWorker(ctx context.Context, gitStore *git.Store, hs *integrity.Store, cfg IntegrityCheckConfig, status *IntegrityStatus, broadcast func(IntegrityAlert), logger *slog.Logger) {
	if !cfg.IsEnabled() {
		logger.Info("integrity check worker disabled")
		return
	}
	interval := cfg.IntervalDuration()
	logger.Info("starting integrity check worker", "interval", interval)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		runCheck(ctx, gitStore, hs, status, broadcast, logger)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCheck(ctx, gitStore, hs, status, broadcast, logger)
			}
		}
	}()
}

func runCheck(ctx context.Context, gitStore *git.Store, hs *integrity.Store, status *IntegrityStatus, broadcast func(IntegrityAlert), logger *slog.Logger) {
	projects, err := gitStore.ListProjects("")
	if err != nil {
		logger.Warn("integrity check: list projects", "error", err)
		return
	}

	var alerts []IntegrityAlert

	for _, p := range projects {
		if ctx.Err() != nil {
			return
		}

		repo, err := gitStore.OpenRepo(p.Namespace, p.Project)
		if err != nil {
			continue
		}
		head, err := repo.Head()
		if err != nil {
			continue
		}
		actual := head.Hash().String()

		stored, err := hs.Get(p.Namespace, p.Project)
		if err != nil {
			continue
		}

		if stored == "" {
			_ = hs.Set(p.Namespace, p.Project, actual)
			continue
		}

		if stored != actual {
			logger.Warn("external modification detected",
				"namespace", p.Namespace, "project", p.Project,
				"expected", stored, "actual", actual)

			_ = hs.Set(p.Namespace, p.Project, actual)

			alert := IntegrityAlert{
				Namespace:    p.Namespace,
				Project:      p.Project,
				ExpectedHead: stored,
				ActualHead:   actual,
				DetectedAt:   time.Now().UTC().Format(time.RFC3339),
			}
			alerts = append(alerts, alert)

			if broadcast != nil {
				broadcast(alert)
			}
		}
	}

	status.record(len(projects), alerts)
}

// recordHeadHash stores the current HEAD hash for a repository.
// Called after every GitYard-mediated write (merge, push, seed pull).
func recordHeadHash(gitStore *git.Store, namespace, project string) {
	if headStore == nil {
		return
	}
	repo, err := gitStore.OpenRepo(namespace, project)
	if err != nil {
		return
	}
	head, err := repo.Head()
	if err != nil {
		return
	}
	_ = headStore.Set(namespace, project, head.Hash().String())
}
