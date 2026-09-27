package quarantine

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// Mark sets com.apple.quarantine: flags;timestamp;agent;event.
func Mark(path, sourceURL string) error {
	value := fmt.Sprintf("0081;%x;godl;", time.Now().Unix())
	return unix.Setxattr(path, "com.apple.quarantine", []byte(value), 0)
}
