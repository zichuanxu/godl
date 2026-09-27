// Package power puts the computer to sleep or shuts it down when the queue
// is done (DESIGN.md milestone M4). It runs the platform's own tools, so no
// privileges beyond the user's are needed.
package power

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

type Action string

const (
	Sleep    Action = "sleep"
	Shutdown Action = "shutdown"
)

// command returns the program and arguments for action on goos.
func command(action Action, goos string) ([]string, error) {
	commands := map[string]map[Action][]string{
		"darwin": {
			Sleep: {"pmset", "sleepnow"},
			// System Events asks running apps to quit, as the Apple menu does.
			Shutdown: {"osascript", "-e", `tell application "System Events" to shut down`},
		},
		"windows": {
			Sleep: {"rundll32.exe", "powrprof.dll,SetSuspendState", "0,1,0"},
			// A short grace period that "shutdown /a" can still abort.
			Shutdown: {"shutdown.exe", "/s", "/t", "30"},
		},
		"linux": {
			Sleep:    {"systemctl", "suspend"},
			Shutdown: {"systemctl", "poweroff"},
		},
	}
	cmd, ok := commands[goos][action]
	if !ok {
		return nil, fmt.Errorf("%s is not supported on %s", action, goos)
	}
	return cmd, nil
}

// Prepare asks for any permission action will need while the user is
// present: on macOS, shutting down through System Events needs Automation
// consent, which a prompt at the end of an unattended queue would never get.
func Prepare(action Action) error {
	if runtime.GOOS != "darwin" || action != Shutdown {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "osascript", "-e", `tell application "System Events" to get name`).CombinedOutput(); err != nil {
		return fmt.Errorf("godl may not control System Events, so it cannot shut down: %w: %s", err, out)
	}
	return nil
}

// Do performs action on this computer.
func Do(action Action) error {
	cmd, err := command(action, runtime.GOOS)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, cmd[0], cmd[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", action, err, out)
	}
	return nil
}
