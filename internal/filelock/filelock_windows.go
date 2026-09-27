//go:build windows

package filelock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func lock(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	if err != nil {
		return fmt.Errorf("lock file: %w", err)
	}
	return nil
}

// unlockAndRemove unlocks and closes before removing, because Go opens files
// without FILE_SHARE_DELETE and Windows refuses to delete an open file. If
// another process has the file open by then, the removal fails and the file is
// left behind, which is harmless.
func unlockAndRemove(f *os.File, path string) error {
	uerr := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
	cerr := f.Close()
	_ = os.Remove(path)
	return errors.Join(uerr, cerr)
}
