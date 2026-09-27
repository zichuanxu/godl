// Package engine adapts the existing trusted downloader to the service model.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/zichuanxu/godl/downloader"
	"github.com/zichuanxu/godl/internal/download"
)

type Runner struct {
	downloader *downloader.Downloader
}

func NewRunner(cfg downloader.Config) (*Runner, error) {
	dl, err := downloader.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Runner{downloader: dl}, nil
}

func (r *Runner) Download(ctx context.Context, rawURL, destination string, progress func(download.Progress)) error {
	err := r.downloader.Download(ctx, rawURL, destination, func(current downloader.Progress) {
		if progress == nil {
			return
		}
		progress(download.Progress{Completed: current.Completed, Total: current.Total})
	})
	if errors.Is(err, downloader.ErrLocked) {
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
