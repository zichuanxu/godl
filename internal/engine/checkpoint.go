package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type intervalCheckpoint struct {
	Version   int         `json:"version"`
	Identity  string      `json:"identity"`
	Size      int64       `json:"size"`
	ETag      string      `json:"etag"`
	Remaining []byteRange `json:"remaining"`
	Completed int64       `json:"completed"`
}

func writeIntervalCheckpoint(path string, state intervalCheckpoint) error {
	if err := validateIntervalCheckpoint(state); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create interval checkpoint: %w", err)
	}
	temporary := file.Name()
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		return fmt.Errorf("encode interval checkpoint: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync interval checkpoint: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close interval checkpoint: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace interval checkpoint: %w", err)
	}
	cleanup = false
	return nil
}

func readIntervalCheckpoint(path string) (intervalCheckpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return intervalCheckpoint{}, fmt.Errorf("read interval checkpoint: %w", err)
	}
	var state intervalCheckpoint
	if err := json.Unmarshal(data, &state); err != nil {
		return intervalCheckpoint{}, fmt.Errorf("decode interval checkpoint: %w", err)
	}
	if err := validateIntervalCheckpoint(state); err != nil {
		return intervalCheckpoint{}, err
	}
	return state, nil
}

func validateIntervalCheckpoint(state intervalCheckpoint) error {
	if state.Version != 1 {
		return fmt.Errorf("unsupported interval checkpoint version %d", state.Version)
	}
	if state.Identity == "" || state.Size <= 0 || state.Completed < 0 || state.Completed > state.Size {
		return errors.New("invalid interval checkpoint identity, size, or completed bytes")
	}
	remaining := append([]byteRange(nil), state.Remaining...)
	sort.Slice(remaining, func(i, j int) bool { return remaining[i].Start < remaining[j].Start })
	var remainingBytes int64
	for i, current := range remaining {
		if current.Start < 0 || current.End < current.Start || current.End >= state.Size {
			return fmt.Errorf("invalid remaining interval %+v", current)
		}
		if i > 0 && remaining[i-1].Overlaps(current) {
			return fmt.Errorf("overlapping remaining intervals %+v and %+v", remaining[i-1], current)
		}
		remainingBytes += current.Length()
	}
	if remainingBytes+state.Completed != state.Size {
		return fmt.Errorf("checkpoint accounts for %d bytes, want %d", remainingBytes+state.Completed, state.Size)
	}
	return nil
}
