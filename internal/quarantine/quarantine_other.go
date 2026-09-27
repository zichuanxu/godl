//go:build !darwin && !windows

package quarantine

// Mark does nothing: other systems have no download quarantine.
func Mark(path, sourceURL string) error { return nil }
