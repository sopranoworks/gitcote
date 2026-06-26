package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/argon2"
	gossh "golang.org/x/crypto/ssh"
)

var (
	bucketVaultMeta = []byte("vault_meta")
	bucketKeys      = []byte("keys")
	keyVaultSalt    = []byte("vault_salt")

	ErrLocked    = errors.New("vault is locked")
	ErrNotFound  = errors.New("key not found")
	ErrDuplicate = errors.New("key already exists")
)

type VaultState int

const (
	VaultLocked   VaultState = iota
	VaultUnlocked
)

type SSHKey struct {
	Name             string    `json:"name"`
	Namespace        string    `json:"namespace"`
	Algorithm        string    `json:"algorithm"`
	EncryptedPrivate []byte    `json:"encrypted_private"`
	PublicKey        string    `json:"public_key"`
	Fingerprint      string    `json:"fingerprint"`
	CreatedAt        time.Time `json:"created_at"`
	CreatedBy        string    `json:"created_by"`
}

type SSHKeyInfo struct {
	Name        string    `json:"name"`
	Namespace   string    `json:"namespace"`
	Algorithm   string    `json:"algorithm"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   string    `json:"created_by"`
}

type Vault struct {
	mu        sync.RWMutex
	state     VaultState
	masterKey []byte
	db        *bolt.DB
}

func Open(dbPath string) (*Vault, error) {
	db, err := bolt.Open(dbPath, 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open vault db: %w", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketVaultMeta); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucketKeys)
		return err
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("init vault buckets: %w", err)
	}
	return &Vault{db: db, state: VaultLocked}, nil
}

func (v *Vault) State() VaultState {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.state
}

func (v *Vault) Unlock(password string) error {
	salt, err := v.ensureVaultSalt()
	if err != nil {
		return fmt.Errorf("vault salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.masterKey = key
	v.state = VaultUnlocked
	return nil
}

func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range v.masterKey {
		v.masterKey[i] = 0
	}
	v.masterKey = nil
	v.state = VaultLocked
}

func (v *Vault) Close() error {
	v.Lock()
	return v.db.Close()
}

func (v *Vault) encrypt(plaintext []byte) ([]byte, error) {
	v.mu.RLock()
	key := v.masterKey
	v.mu.RUnlock()
	if key == nil {
		return nil, ErrLocked
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (v *Vault) decrypt(ciphertext []byte) ([]byte, error) {
	v.mu.RLock()
	key := v.masterKey
	v.mu.RUnlock()
	if key == nil {
		return nil, ErrLocked
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
}

func (v *Vault) ensureVaultSalt() ([]byte, error) {
	var salt []byte
	err := v.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketVaultMeta)
		salt = b.Get(keyVaultSalt)
		if salt != nil {
			salt = append([]byte(nil), salt...)
			return nil
		}
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return err
		}
		return b.Put(keyVaultSalt, salt)
	})
	return salt, err
}

func (v *Vault) GenerateKey(namespace, name, createdBy string) (string, error) {
	if v.State() != VaultUnlocked {
		return "", ErrLocked
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}

	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("convert public key: %w", err)
	}

	pemBlock, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", fmt.Errorf("marshal private key: %w", err)
	}
	pemData := pem.EncodeToMemory(pemBlock)

	encrypted, err := v.encrypt(pemData)
	if err != nil {
		return "", fmt.Errorf("encrypt private key: %w", err)
	}

	pubKeyStr := string(gossh.MarshalAuthorizedKey(sshPub))
	fingerprint := gossh.FingerprintSHA256(sshPub)

	record := SSHKey{
		Name:             name,
		Namespace:        namespace,
		Algorithm:        "ed25519",
		EncryptedPrivate: encrypted,
		PublicKey:        pubKeyStr,
		Fingerprint:      fingerprint,
		CreatedAt:        time.Now().UTC(),
		CreatedBy:        createdBy,
	}

	err = v.db.Update(func(tx *bolt.Tx) error {
		kb := tx.Bucket(bucketKeys)
		nsb, err := kb.CreateBucketIfNotExists([]byte(namespace))
		if err != nil {
			return err
		}
		if nsb.Get([]byte(name)) != nil {
			return ErrDuplicate
		}
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return nsb.Put([]byte(name), data)
	})
	if err != nil {
		return "", err
	}
	return pubKeyStr, nil
}

// ImportKey imports an existing PEM-encoded private key into the vault.
func (v *Vault) ImportKey(namespace, name, createdBy string, privateKeyPEM []byte) (pubKeyStr, fingerprint string, err error) {
	if v.State() != VaultUnlocked {
		return "", "", ErrLocked
	}

	signer, serr := gossh.ParsePrivateKey(privateKeyPEM)
	if serr != nil {
		return "", "", fmt.Errorf("invalid private key: %w", serr)
	}

	sshPub := signer.PublicKey()
	pubKeyStr = string(gossh.MarshalAuthorizedKey(sshPub))
	fingerprint = gossh.FingerprintSHA256(sshPub)

	algo := sshPub.Type()

	encrypted, eerr := v.encrypt(privateKeyPEM)
	if eerr != nil {
		return "", "", fmt.Errorf("encrypt private key: %w", eerr)
	}

	record := SSHKey{
		Name:             name,
		Namespace:        namespace,
		Algorithm:        algo,
		EncryptedPrivate: encrypted,
		PublicKey:        pubKeyStr,
		Fingerprint:      fingerprint,
		CreatedAt:        time.Now().UTC(),
		CreatedBy:        createdBy,
	}

	err = v.db.Update(func(tx *bolt.Tx) error {
		kb := tx.Bucket(bucketKeys)
		nsb, berr := kb.CreateBucketIfNotExists([]byte(namespace))
		if berr != nil {
			return berr
		}
		if nsb.Get([]byte(name)) != nil {
			return ErrDuplicate
		}
		data, jerr := json.Marshal(record)
		if jerr != nil {
			return jerr
		}
		return nsb.Put([]byte(name), data)
	})
	if err != nil {
		return "", "", err
	}
	return pubKeyStr, fingerprint, nil
}

func (v *Vault) ListKeys(namespace string) ([]SSHKeyInfo, error) {
	var keys []SSHKeyInfo
	err := v.db.View(func(tx *bolt.Tx) error {
		kb := tx.Bucket(bucketKeys)
		nsb := kb.Bucket([]byte(namespace))
		if nsb == nil {
			return nil
		}
		return nsb.ForEach(func(k, val []byte) error {
			var rec SSHKey
			if err := json.Unmarshal(val, &rec); err != nil {
				return nil
			}
			keys = append(keys, SSHKeyInfo{
				Name:        rec.Name,
				Namespace:   rec.Namespace,
				Algorithm:   rec.Algorithm,
				PublicKey:    rec.PublicKey,
				Fingerprint: rec.Fingerprint,
				CreatedAt:   rec.CreatedAt,
				CreatedBy:   rec.CreatedBy,
			})
			return nil
		})
	})
	return keys, err
}

func (v *Vault) GetKey(namespace, name string) (*SSHKeyInfo, error) {
	var info *SSHKeyInfo
	err := v.db.View(func(tx *bolt.Tx) error {
		kb := tx.Bucket(bucketKeys)
		nsb := kb.Bucket([]byte(namespace))
		if nsb == nil {
			return ErrNotFound
		}
		data := nsb.Get([]byte(name))
		if data == nil {
			return ErrNotFound
		}
		var rec SSHKey
		if err := json.Unmarshal(data, &rec); err != nil {
			return err
		}
		info = &SSHKeyInfo{
			Name:        rec.Name,
			Namespace:   rec.Namespace,
			Algorithm:   rec.Algorithm,
			PublicKey:    rec.PublicKey,
			Fingerprint: rec.Fingerprint,
			CreatedAt:   rec.CreatedAt,
			CreatedBy:   rec.CreatedBy,
		}
		return nil
	})
	return info, err
}

func (v *Vault) DeleteKey(namespace, name string) error {
	return v.db.Update(func(tx *bolt.Tx) error {
		kb := tx.Bucket(bucketKeys)
		nsb := kb.Bucket([]byte(namespace))
		if nsb == nil {
			return ErrNotFound
		}
		if nsb.Get([]byte(name)) == nil {
			return ErrNotFound
		}
		return nsb.Delete([]byte(name))
	})
}

func (v *Vault) DecryptPrivateKey(namespace, name string) ([]byte, error) {
	var encrypted []byte
	err := v.db.View(func(tx *bolt.Tx) error {
		kb := tx.Bucket(bucketKeys)
		nsb := kb.Bucket([]byte(namespace))
		if nsb == nil {
			return ErrNotFound
		}
		data := nsb.Get([]byte(name))
		if data == nil {
			return ErrNotFound
		}
		var rec SSHKey
		if err := json.Unmarshal(data, &rec); err != nil {
			return err
		}
		encrypted = rec.EncryptedPrivate
		return nil
	})
	if err != nil {
		return nil, err
	}
	return v.decrypt(encrypted)
}
