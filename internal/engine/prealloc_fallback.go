//go:build !darwin && !linux && !windows

package engine

import (
	"fmt"
	"os"
)

func preallocate(file *os.File, size int64) error {
	if err := file.Truncate(size); err != nil {
		return fmt.Errorf("preallocate partial file: %w", err)
	}
	return nil
}
