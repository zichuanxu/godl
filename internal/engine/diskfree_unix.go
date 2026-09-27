//go:build unix

package engine

import "golang.org/x/sys/unix"

// freeSpace returns the bytes available to this user on dir's file system.
func freeSpace(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:unconvert // field types vary by platform
}
