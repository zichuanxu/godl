package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// single downloads over one plain GET for servers without ranges or a known
// size. Each attempt restarts from zero; there is nothing to checkpoint.
func (j *job) single(ctx context.Context, info probeInfo) error {
	e := j.engine
	_ = os.Remove(j.meta)
	if info.size > 0 {
		if free, err := freeSpace(filepath.Dir(j.part)); err == nil && free < info.size+freeSpaceMargin {
			return fmt.Errorf("%w: need %d bytes plus margin, %d available", ErrInsufficientSpace, info.size, free)
		}
	}
	var last *attemptError
	for attempt := 0; attempt < e.cfg.MaxAttempts; attempt++ {
		if last != nil {
			if err := sleepBeforeRetry(ctx, attempt-1, last.retryAfter, e.cfg.BaseBackoff, e.cfg.MaxBackoff); err != nil {
				return err
			}
		}
		last = j.singleOnce(ctx, info)
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !last.retryable {
			break
		}
	}
	return last
}

func (j *job) singleOnce(ctx context.Context, info probeInfo) *attemptError {
	e := j.engine
	f, err := os.OpenFile(j.part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fatal(fmt.Errorf("open partial file: %w", err))
	}
	defer f.Close()
	j.progress.completed.Store(0)
	j.progress.total.Store(info.size)
	if info.size == 0 {
		return closeSynced(f)
	}

	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := newStallTimer(e.cfg.StallTimeout, func() { cancel(ErrStalled) })
	defer stall.Stop()
	reqCtx = stall.trace(reqCtx)
	interrupted := func(err error) *attemptError {
		if errors.Is(context.Cause(reqCtx), ErrStalled) && ctx.Err() == nil {
			err = ErrStalled
		}
		return retryable(fmt.Errorf("download: %w", err))
	}

	req, err := e.newRequest(reqCtx, j.url)
	if err != nil {
		return fatal(err)
	}
	if info.etag != "" {
		req.Header.Set("If-Match", info.etag)
	}
	resp, err := e.cfg.ProbeClient.Do(req)
	if err != nil {
		return interrupted(err)
	}
	defer resp.Body.Close()
	if err := identityEncoding(resp); err != nil {
		return fatal(err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusPreconditionFailed:
		return fatal(ErrSourceChanged)
	default:
		return statusError("download", resp)
	}
	if info.etag != "" && strongETag(resp.Header.Get("ETag")) != info.etag {
		return fatal(ErrSourceChanged)
	}
	total := resp.ContentLength
	j.progress.total.Store(total)

	buf := make([]byte, bufferSize)
	var written int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			stall.Stop()
			if _, err := f.Write(buf[:n]); err != nil {
				return fatal(fmt.Errorf("write partial file: %w", err))
			}
			stall.Reset(e.cfg.StallTimeout)
			written += int64(n)
			j.progress.completed.Store(written)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return interrupted(readErr)
		}
	}
	if total >= 0 && written != total {
		return retryable(fmt.Errorf("short response: got %d bytes, want %d", written, total))
	}
	j.progress.total.Store(written)
	return closeSynced(f)
}

func closeSynced(f *os.File) *attemptError {
	if err := f.Sync(); err != nil {
		return fatal(fmt.Errorf("sync partial file: %w", err))
	}
	if err := f.Close(); err != nil {
		return fatal(fmt.Errorf("close partial file: %w", err))
	}
	return nil
}
