//go:build !unix && !windows

package engine

import "errors"

// freeSpace is unknown here; callers skip the precheck on error.
func freeSpace(string) (int64, error) {
	return 0, errors.New("free space unavailable on this platform")
}
