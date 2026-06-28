package pr

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketPRs     = []byte("prs")
	bucketMeta    = []byte("meta")
	keyNextNumber = []byte("next_number")
)

// Store is a per-project PR store backed by bbolt.
type Store struct {
	db *bolt.DB
}

// Open opens (or creates) a PR store at path.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open pr store: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketPRs); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketMeta); err != nil {
			return err
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the store.
func (s *Store) Close() error { return s.db.Close() }

// Create creates a new PR and returns the assigned number.
func (s *Store) Create(pr *PullRequest) (uint32, error) {
	return pr.Number, s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		prs := tx.Bucket(bucketPRs)

		num := nextNumber(meta)
		pr.Number = num

		data, err := json.Marshal(pr)
		if err != nil {
			return err
		}
		return prs.Put(numKey(num), data)
	})
}

// Get returns a PR by number, or nil if not found.
func (s *Store) Get(number uint32) (*PullRequest, error) {
	var pr PullRequest
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketPRs).Get(numKey(number))
		if data == nil {
			return fmt.Errorf("pr #%d not found", number)
		}
		return json.Unmarshal(data, &pr)
	})
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

// Update updates an existing PR.
func (s *Store) Update(pr *PullRequest) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(pr)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketPRs).Put(numKey(pr.Number), data)
	})
}

// List returns all PRs matching the state filter. Pass "" for all states.
func (s *Store) List(stateFilter PRState) ([]PullRequest, error) {
	var results []PullRequest
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPRs).ForEach(func(_, v []byte) error {
			var pr PullRequest
			if err := json.Unmarshal(v, &pr); err != nil {
				return err
			}
			if stateFilter == "" || pr.State == stateFilter {
				results = append(results, pr)
			}
			return nil
		})
	})
	return results, err
}

// FindByBranches returns an open or approved PR for the given source→target pair, or nil.
func (s *Store) FindByBranches(source, target string) (*PullRequest, error) {
	var found *PullRequest
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPRs).ForEach(func(_, v []byte) error {
			var pr PullRequest
			if err := json.Unmarshal(v, &pr); err != nil {
				return err
			}
			if pr.SourceBranch == source && pr.TargetBranch == target &&
				pr.State != StateMerged && pr.State != StateClosed {
				found = &pr
			}
			return nil
		})
	})
	return found, err
}

// ListByTarget returns all open/approved PRs targeting the given branch.
func (s *Store) ListByTarget(target string) ([]PullRequest, error) {
	var results []PullRequest
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPRs).ForEach(func(_, v []byte) error {
			var pr PullRequest
			if err := json.Unmarshal(v, &pr); err != nil {
				return err
			}
			if pr.TargetBranch == target && (pr.State == StateOpen || pr.State == StateApproved) {
				results = append(results, pr)
			}
			return nil
		})
	})
	return results, err
}

func nextNumber(meta *bolt.Bucket) uint32 {
	raw := meta.Get(keyNextNumber)
	var num uint32 = 1
	if len(raw) == 4 {
		num = binary.BigEndian.Uint32(raw)
	}
	next := num + 1
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, next)
	_ = meta.Put(keyNextNumber, buf)
	return num
}

func numKey(n uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return b
}
