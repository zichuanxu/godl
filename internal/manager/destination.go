package manager

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// resolveRoots returns the absolute, symlink-resolved download roots.
func resolveRoots(roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, errors.New("at least one download root is required")
	}
	resolved := make([]string, 0, len(roots))
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve download root %q: %w", root, err)
		}
		real, err := resolveExisting(abs)
		if err != nil {
			return nil, fmt.Errorf("resolve download root %q: %w", root, err)
		}
		resolved = append(resolved, real)
	}
	return resolved, nil
}

// confine validates that destination resolves, after following symlinks in
// its existing ancestors, to a file inside one of roots and outside known
// autostart locations. It returns the cleaned destination.
func confine(roots []string, destination string) (string, error) {
	if destination == "" {
		return "", errors.New("destination is required")
	}
	if !filepath.IsAbs(destination) {
		return "", errors.New("destination must be an absolute path")
	}
	destination = filepath.Clean(destination)
	if runtime.GOOS == "windows" {
		// Windows ignores trailing dots and spaces and treats ':' as an
		// alternate data stream, so "Startup." or "Startup::$DATA" would alias
		// a denied directory while comparing as a different name.
		for _, part := range strings.Split(destination[len(filepath.VolumeName(destination)):], `\`) {
			if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.Contains(part, ":") {
				return "", fmt.Errorf("destination component %q is not allowed on Windows", part)
			}
		}
	}
	real, err := resolveExisting(destination)
	if err != nil {
		return "", fmt.Errorf("resolve destination: %w", err)
	}
	inside := false
	for _, root := range roots {
		if within(root, real) {
			inside = true
			break
		}
	}
	if !inside {
		return "", fmt.Errorf("destination %s is outside the allowed download roots", destination)
	}
	for _, dir := range autostartDirs() {
		if resolvedDir, err := resolveExisting(dir); err == nil && within(resolvedDir, real) {
			return "", fmt.Errorf("destination %s is inside an autostart location", destination)
		}
	}
	return destination, nil
}

// resolveExisting follows symlinks in the longest existing prefix of path and
// re-appends the components that do not exist yet.
func resolveExisting(path string) (string, error) {
	var missing []string
	for {
		real, err := filepath.EvalSymlinks(path)
		if err == nil {
			return filepath.Join(append([]string{real}, missing...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		missing = append([]string{filepath.Base(path)}, missing...)
		path = parent
	}
}

// foldCase reflects the case-insensitive default file systems of macOS and
// Windows. ponytail: on a case-sensitive APFS volume this treats ~/downloads
// as inside ~/Downloads; both are the same user's siblings, and symlinks are
// already resolved, so it cannot reach an autostart location.
func foldCase(path string) string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

// samePath reports whether two cleaned destinations name the same file.
func samePath(a, b string) bool {
	return foldCase(a) == foldCase(b)
}

// within reports whether path is strictly below dir.
func within(dir, path string) bool {
	dir, path = foldCase(dir), foldCase(path)
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// autostartDirs lists directories where a written file can run code at login.
// ponytail: a best-effort denylist; root confinement is the real control, so
// this only matters when a user configures a broad root such as $HOME.
func autostartDirs() []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	switch runtime.GOOS {
	case "darwin":
		dirs = []string{"/Library/LaunchAgents", "/Library/LaunchDaemons", "/Library/StartupItems"}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "Library", "LaunchAgents"))
		}
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			dirs = append(dirs, filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup"))
		}
		if programData := os.Getenv("ProgramData"); programData != "" {
			dirs = append(dirs, filepath.Join(programData, "Microsoft", "Windows", "Start Menu", "Programs", "StartUp"))
		}
	default:
		dirs = []string{"/etc"}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".config", "autostart"), filepath.Join(home, ".config", "systemd"))
		}
	}
	return dirs
}
