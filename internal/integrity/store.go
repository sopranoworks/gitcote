package integrity

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

import "encoding/json"

var (
	bucketHeads      = []byte("heads")
	bucketTempClones = []byte("temp_clones")
)

// Store persists the last-known HEAD hash for each managed repository
// and tracks seed sync temp clones.
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

func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open integrity store: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketHeads); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucketTempClones)
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
