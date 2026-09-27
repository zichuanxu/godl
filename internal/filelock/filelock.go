// Package filelock provides a non-blocking, exclusive OS advisory lock on a file.
//
// The lock is held on an open file descriptor (flock on Unix, LockFileEx on
// Windows), so the kernel releases it when the holder exits or crashes. A lock
// file left behind on disk therefore never blocks a later Acquire.
package filelock

import (
	"errors"
	"fmt"
	"os"
)

// ErrLocked reports that another process or handle currently holds the lock.
var ErrLocked = errors.New("file is locked")

// Acquire creates or opens path and takes an exclusive advisory lock on it
// without blocking. It returns ErrLocked if another holder exists. The returned
// release function unlocks the file and removes it on a best-effort basis.
func Acquire(path string) (release func() error, err error) {
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open lock file: %w", err)
		}
		if err := lock(f); err != nil {
			_ = f.Close()
			return nil, err
		}
		// A previous holder may have unlinked the file between our open and
		// our lock, leaving us locking an orphaned inode while a new process
		// locks a fresh file at path. Only keep the lock if path still names
		// the file we locked; otherwise retry.
		fi, ferr := f.Stat()
		pi, perr := os.Stat(path)
		if ferr == nil && perr == nil && os.SameFile(fi, pi) {
			return func() error { return unlockAndRemove(f, path) }, nil
		}
		_ = f.Close()
	}
}
