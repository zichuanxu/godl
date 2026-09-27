//go:build darwin

package engine

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func preallocate(file *os.File, size int64) error {
	store := &unix.Fstore_t{
		Flags:   unix.F_ALLOCATEALL,
		Posmode: unix.F_PEOFPOSMODE,
		Length:  size,
	}
	if err := unix.FcntlFstore(file.Fd(), unix.F_PREALLOCATE, store); err != nil {
		if err := file.Truncate(size); err != nil {
			return fmt.Errorf("preallocate partial file: %w", err)
		}
		return nil
	}
	if err := file.Truncate(size); err != nil {
		return fmt.Errorf("size preallocated file: %w", err)
	}
	return nil
}
