// Package secrets seals request headers and credentials stored in SQLite
// (DESIGN.md section 7). The AES-256-GCM key lives in the OS keyring: the
// macOS Keychain, the Windows Credential Manager (DPAPI), or the Secret
// Service on Linux. Without a keyring the key exists only in memory, so
// sealed values written by an earlier process cannot be opened and a resumed
// download asks for its credentials again.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	service = "godl"
	account = "database-key"
	prefix  = "sealed:v1:"
)

// ErrUnreadable reports a sealed value whose key is gone.
var ErrUnreadable = errors.New("sealed value cannot be opened with this key")

type Sealer struct {
	aead cipher.AEAD
	// KeyringErr is set when the key could not be kept in a keyring and lives
	// only in memory.
	KeyringErr error
}

// Open loads the key from the OS keyring, creating it on first use, and falls
// back to an in-memory key when no keyring is available.
func Open() (*Sealer, error) {
	key, keyringErr := keyWithTimeout(keyringTimeout)
	if keyringErr != nil {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	}
	s, err := New(key)
	if err != nil {
		return nil, err
	}
	if keyringErr != nil {
		s.KeyringErr = fmt.Errorf("OS keyring unavailable, stored credentials will not survive a restart: %w", keyringErr)
	}
	return s, nil
}

// keyringTimeout bounds a keyring that never answers, such as a macOS
// keychain waiting on a dialog nobody sees; it leaves time to type a password
// into the prompt that follows an app update.
const keyringTimeout = 60 * time.Second

// keyWithTimeout reads the key, giving up after d.
// ponytail: a timed-out keyring call keeps running in the background.
func keyWithTimeout(d time.Duration) ([]byte, error) {
	type result struct {
		key []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		key, err := keyFromKeyring()
		done <- result{key, err}
	}()
	select {
	case r := <-done:
		return r.key, r.err
	case <-time.After(d):
		return nil, fmt.Errorf("keyring did not answer within %v", d)
	}
}

func keyFromKeyring() ([]byte, error) {
	encoded, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := keyring.Set(service, account, base64.StdEncoding.EncodeToString(key)); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("keyring holds a malformed godl key")
	}
	return key, nil
}

// New seals with a 32-byte key.
func New(key []byte) (*Sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext; the empty string stays empty.
func (s *Sealer) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(plaintext), []byte(prefix))
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a sealed value. Values without the sealed prefix, written
// before encryption existed, are returned as they are.
func (s *Sealer) Open(value string) (string, error) {
	encoded, ok := strings.CutPrefix(value, prefix)
	if !ok {
		return value, nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) < s.aead.NonceSize() {
		return "", ErrUnreadable
	}
	plain, err := s.aead.Open(nil, data[:s.aead.NonceSize()], data[s.aead.NonceSize():], []byte(prefix))
	if err != nil {
		return "", ErrUnreadable
	}
	return string(plain), nil
}
