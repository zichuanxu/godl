package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"

	"github.com/zichuanxu/godl/internal/download"
)

// Runner adapts the engine to the service's download.Runner contract. Every
// download shares the base configuration's HTTP clients and connection pools.
type Runner struct {
	base Config
}

func NewRunner(cfg Config) (*Runner, error) {
	if cfg.ProbeClient == nil {
		cfg.ProbeClient = newProbeClient(cfg.Proxy)
	}
	if cfg.SegmentClient == nil {
		cfg.SegmentClient = newSegmentClient(2*maxConnections, cfg.Proxy)
	}
	if _, err := New(cfg); err != nil {
		return nil, err
	}
	return &Runner{base: cfg}, nil
}

func (r *Runner) Inspect(ctx context.Context, rawURL string, headers http.Header) (download.Remote, error) {
	cfg := r.base
	cfg.Headers = headers
	e, err := New(cfg)
	if err != nil {
		return download.Remote{}, err
	}
	remote, err := e.Inspect(ctx, rawURL)
	if err != nil {
		return download.Remote{}, err
	}
	return download.Remote{Filename: remote.Filename, URL: remote.URL, ContentType: remote.ContentType, Size: remote.Size}, nil
}

func (r *Runner) Download(ctx context.Context, spec download.Spec, progress func(download.Progress)) error {
	cfg := r.base
	if spec.Connections > 0 {
		cfg.Connections = spec.Connections
	}
	cfg.Headers = spec.Headers
	cfg.Checksum = spec.Checksum
	cfg.Limiters = spec.Limiters
	e, err := New(cfg)
	if err != nil {
		return err
	}
	err = e.Download(ctx, spec.URL, spec.Destination, func(current Progress) {
		if progress != nil {
			progress(download.Progress{Completed: current.Completed, Total: current.Total})
		}
	})
	if errors.Is(err, ErrLocked) {
		// Windows may release a killed process's lock a moment after it exits.
		return fmt.Errorf("%w: %w", download.ErrDestinationBusy, err)
	}
	return err
}

// Discard removes the downloader's resumable partial state for destination.
func (r *Runner) Discard(destination string) error {
	var errs []error
	for _, path := range []string{destination + ".part", destination + ".part.meta"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
