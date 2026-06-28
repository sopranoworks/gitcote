package integrity

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketHeads = []byte("heads")

// Store persists the last-known HEAD hash for each managed repository.
type Store struct {
	db *bolt.DB
}

func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open integrity store: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketHeads)
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
