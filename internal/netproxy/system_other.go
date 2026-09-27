//go:build !darwin && !windows

package netproxy

// readSystem reports no OS-level settings; Func falls back to the environment.
func readSystem() (*settings, error) { return nil, nil }
