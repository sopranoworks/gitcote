package integrity

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

import "encoding/json"

var (
	bucketHeads             = []byte("heads")
	bucketTempClones        = []byte("temp_clones")
	bucketAgentWorkdirs     = []byte("agent_workdirs")
	bucketAgentTokens       = []byte("agent_tokens")
	bucketPREventSettings   = []byte("pr_event_settings")
	bucketSeedEventSettings = []byte("seed_event_settings")
)

// Store persists the last-known HEAD hash for each managed repository,
// tracks seed sync temp clones, and agent workdir records.
type Store struct {
	db *bolt.DB
}

// TempCloneRecord tracks a temp clone created for seed conflict resolution.
type TempCloneRecord struct {
	Namespace string `json:"namespace"`
	Project   string `json:"project"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
}

// AgentTokenRecord tracks an issued agent token for mutual exclusion.
type AgentTokenRecord struct {
	SeriesID  string `json:"series_id"`
	Namespace string `json:"namespace"`
	Project   string `json:"project"`
	PRNumber  int    `json:"pr_number"`
	TaskType  string `json:"task_type"`
	AgentName string `json:"agent_name"`
	Role      string `json:"role"`
	IssuedAt  string `json:"issued_at"`
}

// AgentWorkdirRecord tracks an agent working directory for lifecycle management.
type AgentWorkdirRecord struct {
	Path      string `json:"path"`
	AgentName string `json:"agent_name"`
	Role      string `json:"role"`
	Namespace string `json:"namespace"`
	Project   string `json:"project"`
	PRNumber  int    `json:"pr_number"`
	CreatedAt string `json:"created_at"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
}

func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open integrity store: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketHeads, bucketTempClones, bucketAgentWorkdirs, bucketAgentTokens, bucketPREventSettings, bucketSeedEventSettings} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Get returns the stored HEAD hash for a repository, or "" if not yet recorded.
func (s *Store) Get(namespace, project string) (string, error) {
	key := namespace + "/" + project
	var hash string
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketHeads).Get([]byte(key))
		if v != nil {
			hash = string(v)
		}
		return nil
	})
	return hash, err
}

// Set records the HEAD hash for a repository.
func (s *Store) Set(namespace, project, hash string) error {
	key := namespace + "/" + project
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketHeads).Put([]byte(key), []byte(hash))
	})
}

// AddTempClone records a temp clone for tracking and cleanup.
func (s *Store) AddTempClone(rec TempCloneRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketTempClones).Put([]byte(rec.Path), data)
	})
}

// ListTempClones returns all tracked temp clones.
func (s *Store) ListTempClones() ([]TempCloneRecord, error) {
	var recs []TempCloneRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTempClones).ForEach(func(_, v []byte) error {
			var rec TempCloneRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			recs = append(recs, rec)
			return nil
		})
	})
	return recs, err
}

// RemoveTempClone removes a temp clone record by path.
func (s *Store) RemoveTempClone(path string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTempClones).Delete([]byte(path))
	})
}

// AddAgentWorkdir records an agent working directory.
func (s *Store) AddAgentWorkdir(rec AgentWorkdirRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketAgentWorkdirs).Put([]byte(rec.Path), data)
	})
}

// UpdateAgentWorkdir updates the status and exit code of an agent workdir record.
func (s *Store) UpdateAgentWorkdir(path, status string, exitCode int) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAgentWorkdirs)
		v := b.Get([]byte(path))
		if v == nil {
			return fmt.Errorf("agent workdir not found: %s", path)
		}
		var rec AgentWorkdirRecord
		if err := json.Unmarshal(v, &rec); err != nil {
			return err
		}
		rec.Status = status
		rec.ExitCode = exitCode
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return b.Put([]byte(path), data)
	})
}

// ListAgentWorkdirs returns all tracked agent working directories.
func (s *Store) ListAgentWorkdirs() ([]AgentWorkdirRecord, error) {
	var recs []AgentWorkdirRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAgentWorkdirs).ForEach(func(_, v []byte) error {
			var rec AgentWorkdirRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			recs = append(recs, rec)
			return nil
		})
	})
	return recs, err
}

// RemoveAgentWorkdir removes an agent workdir record by path.
func (s *Store) RemoveAgentWorkdir(path string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAgentWorkdirs).Delete([]byte(path))
	})
}

// --- Event Settings ---

// EventAction configures what happens on a PR event.
type EventAction struct {
	AgentEnabled  *bool  `json:"agent_enabled,omitempty"`
	AgentName     string `json:"agent_name,omitempty"`
	AutoRetry     *bool  `json:"auto_retry,omitempty"`
	MaxRetries    *int   `json:"max_retries,omitempty"`
	NotifyEnabled *bool  `json:"notify_enabled,omitempty"`
	NotifyMethod  string `json:"notify_method,omitempty"`
}

// ConfirmAction configures what happens when a PR is approved.
type ConfirmAction struct {
	AutoConfirm   *bool  `json:"auto_confirm,omitempty"`
	NotifyEnabled *bool  `json:"notify_enabled,omitempty"`
	NotifyMethod  string `json:"notify_method,omitempty"`
}

// PREventSettings configures PR lifecycle event hooks.
type PREventSettings struct {
	OnCreated       *EventAction   `json:"on_created,omitempty"`
	OnConfirmed     *ConfirmAction `json:"on_confirmed,omitempty"`
	OnRejected      *EventAction   `json:"on_rejected,omitempty"`
	OnMergeConflict *EventAction   `json:"on_merge_conflict,omitempty"`
}

// SeedEventSettings configures seed sync event hooks.
type SeedEventSettings struct {
	OnPushConflict *EventAction `json:"on_push_conflict,omitempty"`
	OnPullConflict *EventAction `json:"on_pull_conflict,omitempty"`
}

func boolVal(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func intVal(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// ResolvedEventAction returns an EventAction with defaults applied.
type ResolvedEventAction struct {
	AgentEnabled  bool
	AgentName     string
	AutoRetry     bool
	MaxRetries    int
	NotifyEnabled bool
	NotifyMethod  string
}

// Resolve merges project → global → hardcoded defaults.
func ResolveEventAction(project, global *EventAction) ResolvedEventAction {
	r := ResolvedEventAction{NotifyMethod: "log"}
	if global != nil {
		r.AgentEnabled = boolVal(global.AgentEnabled, false)
		if global.AgentName != "" {
			r.AgentName = global.AgentName
		}
		r.AutoRetry = boolVal(global.AutoRetry, false)
		r.MaxRetries = intVal(global.MaxRetries, 0)
		r.NotifyEnabled = boolVal(global.NotifyEnabled, false)
		if global.NotifyMethod != "" {
			r.NotifyMethod = global.NotifyMethod
		}
	}
	if project != nil {
		if project.AgentEnabled != nil {
			r.AgentEnabled = *project.AgentEnabled
		}
		if project.AgentName != "" {
			r.AgentName = project.AgentName
		}
		if project.AutoRetry != nil {
			r.AutoRetry = *project.AutoRetry
		}
		if project.MaxRetries != nil {
			r.MaxRetries = *project.MaxRetries
		}
		if project.NotifyEnabled != nil {
			r.NotifyEnabled = *project.NotifyEnabled
		}
		if project.NotifyMethod != "" {
			r.NotifyMethod = project.NotifyMethod
		}
	}
	return r
}

// ResolvedConfirmAction returns a ConfirmAction with defaults applied.
type ResolvedConfirmAction struct {
	AutoConfirm   bool
	NotifyEnabled bool
	NotifyMethod  string
}

// ResolveConfirmAction merges project → global → hardcoded defaults.
func ResolveConfirmAction(project, global *ConfirmAction) ResolvedConfirmAction {
	r := ResolvedConfirmAction{NotifyMethod: "log"}
	if global != nil {
		r.AutoConfirm = boolVal(global.AutoConfirm, false)
		r.NotifyEnabled = boolVal(global.NotifyEnabled, false)
		if global.NotifyMethod != "" {
			r.NotifyMethod = global.NotifyMethod
		}
	}
	if project != nil {
		if project.AutoConfirm != nil {
			r.AutoConfirm = *project.AutoConfirm
		}
		if project.NotifyEnabled != nil {
			r.NotifyEnabled = *project.NotifyEnabled
		}
		if project.NotifyMethod != "" {
			r.NotifyMethod = project.NotifyMethod
		}
	}
	return r
}

const (
	keyGlobalPREventSettings   = "pr_event_settings:global"
	keyGlobalSeedEventSettings = "seed_event_settings:global"
)

func projectPREventKey(ns, proj string) string   { return "pr_event_settings:" + ns + "/" + proj }
func projectSeedEventKey(ns, proj string) string  { return "seed_event_settings:" + ns + "/" + proj }

// GetPREventSettings returns the settings for a given key (global or project).
func (s *Store) GetPREventSettings(key string) (*PREventSettings, error) {
	var settings *PREventSettings
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketPREventSettings).Get([]byte(key))
		if v == nil {
			return nil
		}
		settings = &PREventSettings{}
		return json.Unmarshal(v, settings)
	})
	return settings, err
}

// SetPREventSettings stores settings for a given key.
func (s *Store) SetPREventSettings(key string, settings *PREventSettings) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketPREventSettings).Put([]byte(key), data)
	})
}

// DeletePREventSettings removes settings for a given key.
func (s *Store) DeletePREventSettings(key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPREventSettings).Delete([]byte(key))
	})
}

// ResolvePREventSettings returns the merged settings (project → global → defaults).
func (s *Store) ResolvePREventSettings(ns, proj string) (*PREventSettings, error) {
	global, err := s.GetPREventSettings(keyGlobalPREventSettings)
	if err != nil {
		return nil, err
	}
	project, err := s.GetPREventSettings(projectPREventKey(ns, proj))
	if err != nil {
		return nil, err
	}
	if project != nil {
		return project, nil
	}
	if global != nil {
		return global, nil
	}
	return &PREventSettings{}, nil
}

// GetGlobalPREventSettings returns the global settings.
func (s *Store) GetGlobalPREventSettings() (*PREventSettings, error) {
	return s.GetPREventSettings(keyGlobalPREventSettings)
}

// SetGlobalPREventSettings stores the global settings.
func (s *Store) SetGlobalPREventSettings(settings *PREventSettings) error {
	return s.SetPREventSettings(keyGlobalPREventSettings, settings)
}

// GetProjectPREventSettings returns the per-project override.
func (s *Store) GetProjectPREventSettings(ns, proj string) (*PREventSettings, error) {
	return s.GetPREventSettings(projectPREventKey(ns, proj))
}

// SetProjectPREventSettings stores the per-project override.
func (s *Store) SetProjectPREventSettings(ns, proj string, settings *PREventSettings) error {
	return s.SetPREventSettings(projectPREventKey(ns, proj), settings)
}

// ClearProjectPREventSettings removes the per-project override.
func (s *Store) ClearProjectPREventSettings(ns, proj string) error {
	return s.DeletePREventSettings(projectPREventKey(ns, proj))
}

// GetSeedEventSettings returns the settings for a given key.
func (s *Store) GetSeedEventSettings(key string) (*SeedEventSettings, error) {
	var settings *SeedEventSettings
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketSeedEventSettings).Get([]byte(key))
		if v == nil {
			return nil
		}
		settings = &SeedEventSettings{}
		return json.Unmarshal(v, settings)
	})
	return settings, err
}

// SetSeedEventSettings stores settings for a given key.
func (s *Store) SetSeedEventSettings(key string, settings *SeedEventSettings) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketSeedEventSettings).Put([]byte(key), data)
	})
}

// DeleteSeedEventSettings removes settings for a given key.
func (s *Store) DeleteSeedEventSettings(key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSeedEventSettings).Delete([]byte(key))
	})
}

// GetGlobalSeedEventSettings returns the global settings.
func (s *Store) GetGlobalSeedEventSettings() (*SeedEventSettings, error) {
	return s.GetSeedEventSettings(keyGlobalSeedEventSettings)
}

// SetGlobalSeedEventSettings stores the global settings.
func (s *Store) SetGlobalSeedEventSettings(settings *SeedEventSettings) error {
	return s.SetSeedEventSettings(keyGlobalSeedEventSettings, settings)
}

// GetProjectSeedEventSettings returns the per-project override.
func (s *Store) GetProjectSeedEventSettings(ns, proj string) (*SeedEventSettings, error) {
	return s.GetSeedEventSettings(projectSeedEventKey(ns, proj))
}

// SetProjectSeedEventSettings stores the per-project override.
func (s *Store) SetProjectSeedEventSettings(ns, proj string, settings *SeedEventSettings) error {
	return s.SetSeedEventSettings(projectSeedEventKey(ns, proj), settings)
}

// ClearProjectSeedEventSettings removes the per-project override.
func (s *Store) ClearProjectSeedEventSettings(ns, proj string) error {
	return s.DeleteSeedEventSettings(projectSeedEventKey(ns, proj))
}

// ResolveSeedEventSettings returns the merged settings (project → global → defaults).
func (s *Store) ResolveSeedEventSettings(ns, proj string) (*SeedEventSettings, error) {
	global, err := s.GetSeedEventSettings(keyGlobalSeedEventSettings)
	if err != nil {
		return nil, err
	}
	project, err := s.GetSeedEventSettings(projectSeedEventKey(ns, proj))
	if err != nil {
		return nil, err
	}
	if project != nil {
		return project, nil
	}
	if global != nil {
		return global, nil
	}
	return &SeedEventSettings{}, nil
}

// GetAgentWorkdir returns a single agent workdir record by path.
func (s *Store) GetAgentWorkdir(path string) (*AgentWorkdirRecord, error) {
	var rec *AgentWorkdirRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketAgentWorkdirs).Get([]byte(path))
		if v == nil {
			return nil
		}
		r := AgentWorkdirRecord{}
		if err := json.Unmarshal(v, &r); err != nil {
			return err
		}
		rec = &r
		return nil
	})
	return rec, err
}

// --- Agent Token Records ---

// GetAgentToken returns the token record for a given key, or nil.
func (s *Store) GetAgentToken(key string) (*AgentTokenRecord, error) {
	var rec *AgentTokenRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketAgentTokens).Get([]byte(key))
		if v == nil {
			return nil
		}
		r := AgentTokenRecord{}
		if err := json.Unmarshal(v, &r); err != nil {
			return err
		}
		rec = &r
		return nil
	})
	return rec, err
}

// SetAgentToken stores an agent token record.
func (s *Store) SetAgentToken(key string, rec AgentTokenRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketAgentTokens).Put([]byte(key), data)
	})
}

// RemoveAgentToken removes an agent token record by key.
func (s *Store) RemoveAgentToken(key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAgentTokens).Delete([]byte(key))
	})
}

// ListAgentTokens returns all tracked agent token records.
func (s *Store) ListAgentTokens() ([]AgentTokenRecord, error) {
	var recs []AgentTokenRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAgentTokens).ForEach(func(_, v []byte) error {
			var rec AgentTokenRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			recs = append(recs, rec)
			return nil
		})
	})
	return recs, err
}
