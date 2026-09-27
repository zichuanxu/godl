package main

import (
	"context"
	"log"
	"os"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
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

// optionalNotifier starts the notification service without letting its
// failure stop the app: Wails aborts when any service fails to start.
type optionalNotifier struct {
	*notifications.NotificationService
	desk   *Desktop
	failed bool
}

func (o *optionalNotifier) ServiceStartup(ctx context.Context, opts application.ServiceOptions) error {
	if err := o.NotificationService.ServiceStartup(ctx, opts); err != nil {
		log.Printf("notifications unavailable: %v", err)
		o.failed = true
		o.desk.notifier.Store(nil)
	}
	return nil
}

func (o *optionalNotifier) ServiceShutdown() error {
	if o.failed {
		return nil
	}
	return o.NotificationService.ServiceShutdown()
}
