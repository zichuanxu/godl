//go:build linux

package engine

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func preallocate(file *os.File, size int64) error {
	if err := unix.Fallocate(int(file.Fd()), 0, 0, size); err != nil {
		if err := file.Truncate(size); err != nil {
			return fmt.Errorf("preallocate partial file: %w", err)
		}
		return nil
	}
	return file.Truncate(size)
}
