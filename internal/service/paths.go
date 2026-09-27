package service

import (
	"fmt"
	"os"
	"path/filepath"
)

func DefaultDatabasePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	dir := filepath.Join(configDir, "godl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create application data directory: %w", err)
	}
	return filepath.Join(dir, "godl.db"), nil
}
