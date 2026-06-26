// Package sshkeys stores user SSH public keys for inbound SSH authentication.
// Keys are indexed by fingerprint for O(1) lookup during the SSH handshake.
package sshkeys

import (
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
	bolt "go.etcd.io/bbolt"
)

var keysBucket = []byte("keys")

type KeyRecord struct {
	Fingerprint string `json:"fingerprint"`
	Email       string `json:"email"`
	KeyType     string `json:"key_type"`
	PublicKey   string `json:"public_key"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"`
}

type Store struct {
	db *bolt.DB
}

func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(keysBucket)
		return err
	}); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Add registers a user's SSH public key. The publicKeyData is the full
// authorized_keys line (e.g. "ssh-ed25519 AAAA... user@host").
func (s *Store) Add(email, publicKeyData, title string) (string, error) {
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKeyData))
	if err != nil {
		return "", fmt.Errorf("invalid public key: %w", err)
	}

	fp := ssh.FingerprintSHA256(pubKey)

	rec := KeyRecord{
		Fingerprint: fp,
		Email:       email,
		KeyType:     pubKey.Type(),
		PublicKey:   publicKeyData,
		Title:       title,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}

	return fp, s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(keysBucket)
		if existing := b.Get([]byte(fp)); existing != nil {
			return fmt.Errorf("key already registered")
		}
		return b.Put([]byte(fp), data)
	})
}

// LookupByKey finds the user associated with a public key offered during
// SSH authentication. Returns the email and true if found.
func (s *Store) LookupByKey(pubKey ssh.PublicKey) (string, bool) {
	fp := ssh.FingerprintSHA256(pubKey)
	var email string
	_ = s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(keysBucket).Get([]byte(fp))
		if data == nil {
			return nil
		}
		var rec KeyRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil
		}
		email = rec.Email
		return nil
	})
	return email, email != ""
}

// ListByUser returns all SSH keys registered by a user.
func (s *Store) ListByUser(email string) ([]KeyRecord, error) {
	var keys []KeyRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(keysBucket).ForEach(func(_, v []byte) error {
			var rec KeyRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil
			}
			if rec.Email == email {
				keys = append(keys, rec)
			}
			return nil
		})
	})
	return keys, err
}

// Delete removes a user's SSH key by fingerprint.
func (s *Store) Delete(email, fingerprint string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(keysBucket)
		data := b.Get([]byte(fingerprint))
		if data == nil {
			return fmt.Errorf("key not found")
		}
		var rec KeyRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			return err
		}
		if rec.Email != email {
			return fmt.Errorf("key does not belong to this user")
		}
		return b.Delete([]byte(fingerprint))
	})
}
