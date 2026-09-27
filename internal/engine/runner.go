// Package engine adapts the existing trusted downloader to the service model.
package engine

import (
	"context"

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
	return r.downloader.Download(ctx, rawURL, destination, func(current downloader.Progress) {
		if progress == nil {
			return
		}
		progress(download.Progress{Completed: current.Completed, Total: current.Total})
	})
}
