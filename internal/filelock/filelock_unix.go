//go:build unix

package filelock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrLocked
	}
	if err != nil {
		return fmt.Errorf("lock file: %w", err)
	}
	return nil
}

// unlockAndRemove unlinks the file while still holding the lock, then closes
// it (which drops the flock). A contender that opened the old inode will see
// the path gone or replaced after locking and retry, so the unlink cannot let
// two holders coexist.
func unlockAndRemove(f *os.File, path string) error {
	_ = os.Remove(path)
	return f.Close()
}
