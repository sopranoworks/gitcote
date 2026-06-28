package integrity

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

import "encoding/json"

var (
	bucketHeads         = []byte("heads")
	bucketTempClones    = []byte("temp_clones")
	bucketAgentWorkdirs = []byte("agent_workdirs")
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
		if _, err := tx.CreateBucketIfNotExists(bucketHeads); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketTempClones); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucketAgentWorkdirs)
		return err
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
