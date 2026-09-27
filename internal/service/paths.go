package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func dataDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	dir := filepath.Join(configDir, "godl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create application data directory: %w", err)
	}
	return dir, nil
}

func DefaultDatabasePath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "godl.db"), nil
}

// DefaultTokenPath is where the service stores, and the CLI reads, the API token.
func DefaultTokenPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "token"), nil
}

func DefaultDownloadRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, "Downloads"), nil
}

// LoadOrCreateToken returns the token stored at path, creating a random one on
// first use. ponytail: mode 0600 protects it on Unix; on Windows it relies on
// the per-user ACL of the %AppData% directory instead.
func LoadOrCreateToken(path string) (string, error) {
	token, err := ReadToken(path)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return token, err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate service token: %w", err)
	}
	token = hex.EncodeToString(raw[:])
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create token directory: %w", err)
	}
	// Write a complete temp file (CreateTemp uses mode 0600), then hard-link
	// it into place: the link either publishes the whole token or fails
	// because another service won the race, so no reader or crash can ever
	// leave an empty token file behind.
	tmp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return "", fmt.Errorf("create service token: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.WriteString(token + "\n")
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("write service token: %w", werr)
	}
	if err := os.Link(tmp.Name(), path); errors.Is(err, fs.ErrExist) {
		return ReadToken(path)
	} else if err != nil {
		return "", fmt.Errorf("publish service token: %w", err)
	}
	return token, nil
}

// ReadToken reads the service token. A missing file wraps fs.ErrNotExist.
func ReadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read service token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("service token file %s is empty", path)
	}
	return token, nil
}
