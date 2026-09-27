//go:build windows

package engine

import "golang.org/x/sys/windows"

// freeSpace returns the bytes available to this user on dir's volume.
func freeSpace(dir string) (int64, error) {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, &total, &free); err != nil {
		return 0, err
	}
	return int64(available), nil
}
