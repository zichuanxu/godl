package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/zichuanxu/godl/internal/hls"
)

// errPlaylist reports that the probed resource is an HLS playlist.
var errPlaylist = errors.New("resource is an HLS playlist")

// downloadHLS fetches a playlist's segments into dest with the engine's
// client, headers, retries, and limiters. Resume works per segment.
func (e *Engine) downloadHLS(ctx context.Context, rawURL, dest string, onProgress func(Progress)) error {
	if e.sum != nil {
		return errors.New("checksums are not supported for HLS streams")
	}
	if e.cfg.Overwrite {
		if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	headers := e.cfg.Headers.Clone()
	for _, name := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		headers.Del(name)
	}
	limiters := make([]hls.Limiter, len(e.cfg.Limiters))
	for i, l := range e.cfg.Limiters {
		limiters[i] = l
	}
	opts := hls.Options{
		// The probe client negotiates HTTP/2, which suits many small requests.
		Client: e.cfg.ProbeClient, Headers: headers, UserAgent: e.cfg.UserAgent,
		Concurrency: min(e.cfg.Connections, 8), MaxAttempts: e.cfg.MaxAttempts,
		BaseBackoff: e.cfg.BaseBackoff, MaxBackoff: e.cfg.MaxBackoff, StallTimeout: e.cfg.StallTimeout,
		Limiters: limiters,
	}
	var progress func(hls.Progress)
	if onProgress != nil {
		progress = func(p hls.Progress) { onProgress(Progress{Completed: p.Completed, Total: p.Total}) }
	}
	err := hls.Download(ctx, rawURL, dest, opts, progress)
	switch {
	case errors.Is(err, hls.ErrLocked):
		return fmt.Errorf("%w: %s", ErrLocked, dest)
	case errors.Is(err, hls.ErrDestinationExists):
		return fmt.Errorf("%w: %s", ErrDestinationExists, dest)
	}
	return err
}
