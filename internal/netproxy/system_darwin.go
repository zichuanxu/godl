package netproxy

import (
	"context"
	"os/exec"
	"time"
)

// readSystem reads the SystemConfiguration proxy settings via scutil, which
// avoids cgo. A nil result means no proxy is enabled.
func readSystem() (*settings, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return nil, err
	}
	return parseScutil(string(out)), nil
}
