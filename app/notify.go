package main

import (
	"os"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// newNotifier returns the notification service, or nil where it cannot
// start: macOS delivers notifications only to bundled apps, so a bare
// binary (go run, the dev build) runs without them.
func newNotifier() *notifications.NotificationService {
	if runtime.GOOS == "darwin" {
		exe, err := os.Executable()
		if err != nil || !strings.Contains(exe, ".app/Contents/MacOS/") {
			return nil
		}
	}
	return notifications.New()
}
