// Package remux converts a finished MPEG-TS download to MP4 with the user's
// own ffmpeg (DESIGN.md 3.10). ffmpeg is never bundled; it runs as a child
// process so its failures stay isolated.
package remux

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNoFFmpeg reports that no ffmpeg executable was found.
var ErrNoFFmpeg = errors.New("ffmpeg was not found; install it or set its path in Settings")

// Find returns the ffmpeg to use: the configured path, else ffmpeg on PATH.
func Find(configured string) (string, error) {
	if configured != "" {
		if fi, err := os.Stat(configured); err != nil || fi.IsDir() {
			return "", fmt.Errorf("%w (%s)", ErrNoFFmpeg, configured)
		}
		return configured, nil
	}
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", ErrNoFFmpeg
	}
	return path, nil
}

// Output is the MP4 path for a .ts input.
func Output(input string) string {
	return strings.TrimSuffix(input, filepath.Ext(input)) + ".mp4"
}

// ToMP4 copies the streams of input into a new MP4 without re-encoding. It
// never overwrites an existing file.
func ToMP4(ctx context.Context, ffmpeg, input string) (string, error) {
	out := Output(input)
	if _, err := os.Lstat(out); err == nil {
		return "", fmt.Errorf("%s already exists", out)
	}
	// Written under a temporary name so a failed run leaves no broken MP4.
	tmp := out + ".tmp.mp4"
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-i", input, "-c", "copy", "-movflags", "+faststart", tmp)
	if msg, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(msg)))
	}
	// Linking fails if out appeared meanwhile; file systems without links
	// fall back to a rename.
	if err := os.Link(tmp, out); err == nil {
		_ = os.Remove(tmp)
	} else if errors.Is(err, fs.ErrExist) {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("%s already exists", out)
	} else if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("place %s: %w", out, err)
	}
	return out, nil
}
