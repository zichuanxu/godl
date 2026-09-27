package hls

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxResource bounds a single playlist, key or segment held in memory.
const maxResource = 512 << 20

var errStalled = errors.New("connection stalled")

// get fetches uri, or its byte range [off, off+n) when n >= 0, retrying
// transient failures. It returns the body and the URL after redirects. When
// counted, received bytes feed the progress tracker.
func (d *downloader) get(ctx context.Context, uri string, off, n int64, counted bool) ([]byte, *url.URL, error) {
	var last *attemptError
	for attempt := 0; attempt < d.opts.MaxAttempts; attempt++ {
		if last != nil {
			if err := sleepBeforeRetry(ctx, attempt-1, last.retryAfter, d.opts.BaseBackoff, d.opts.MaxBackoff); err != nil {
				return nil, nil, err
			}
		}
		var got int64
		body, final, aerr := d.getOnce(ctx, uri, off, n, func(k int) {
			if counted {
				got += int64(k)
				d.prog.add(int64(k))
			}
		})
		if aerr == nil {
			return body, final, nil
		}
		d.prog.add(-got)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if last = aerr; !aerr.retryable {
			break
		}
	}
	return nil, nil, last
}

func (d *downloader) getOnce(ctx context.Context, uri string, off, n int64, onRead func(int)) ([]byte, *url.URL, *attemptError) {
	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := newStallTimer(d.opts.StallTimeout, func() { cancel(errStalled) })
	defer stall.Stop()
	interrupted := func(err error) *attemptError {
		if errors.Is(context.Cause(reqCtx), errStalled) && ctx.Err() == nil {
			err = errStalled
		}
		return retryable(fmt.Errorf("GET %s: %w", uri, err))
	}

	req, err := http.NewRequestWithContext(stall.trace(reqCtx), http.MethodGet, uri, nil)
	if err != nil {
		return nil, nil, fatal(err)
	}
	for k, v := range d.opts.Headers {
		req.Header[k] = slices.Clone(v)
	}
	req.Header.Set("User-Agent", d.opts.UserAgent)
	req.Header.Set("Accept-Encoding", "identity")
	if n >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+n-1))
	}
	resp, err := d.opts.Client.Do(req)
	if err != nil {
		return nil, nil, interrupted(err)
	}
	defer resp.Body.Close()
	if enc := strings.TrimSpace(resp.Header.Get("Content-Encoding")); enc != "" && !strings.EqualFold(enc, "identity") {
		return nil, nil, fatal(fmt.Errorf("GET %s: unsupported Content-Encoding %q", uri, enc))
	}
	skip, want := int64(0), n
	switch {
	case resp.StatusCode == http.StatusPartialContent && n >= 0:
	case resp.StatusCode == http.StatusOK && n >= 0:
		skip = off // the server ignored Range; cut the sub-range out ourselves
	case resp.StatusCode == http.StatusOK:
		want = resp.ContentLength
	default:
		return nil, nil, statusError(uri, resp)
	}
	if want > maxResource {
		return nil, nil, fatal(fmt.Errorf("GET %s: %d bytes exceeds the %d-byte limit", uri, want, maxResource))
	}

	var buf bytes.Buffer
	chunk := make([]byte, d.readSize())
	for want < 0 || int64(buf.Len()) < want {
		k, readErr := resp.Body.Read(chunk)
		if k > 0 {
			stall.Stop() // waiting on a rate limiter is not a stall
			for _, l := range d.opts.Limiters {
				if err := l.WaitN(reqCtx, k); err != nil {
					if reqCtx.Err() != nil {
						return nil, nil, interrupted(err)
					}
					return nil, nil, fatal(fmt.Errorf("rate limiter: %w", err))
				}
			}
			stall.Reset(d.opts.StallTimeout)
			keep := chunk[min(int64(k), skip):k]
			skip -= int64(k - len(keep))
			if want >= 0 {
				keep = keep[:min(int64(len(keep)), want-int64(buf.Len()))]
			}
			buf.Write(keep)
			onRead(len(keep))
			if buf.Len() > maxResource {
				return nil, nil, fatal(fmt.Errorf("GET %s: body exceeds the %d-byte limit", uri, maxResource))
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, nil, interrupted(readErr)
		}
	}
	if want >= 0 && int64(buf.Len()) != want {
		return nil, nil, retryable(fmt.Errorf("GET %s: short response: got %d bytes, want %d", uri, buf.Len(), want))
	}
	return buf.Bytes(), resp.Request.URL, nil
}

// readSize keeps each read within every limiter's burst so WaitN never fails.
func (d *downloader) readSize() int {
	size := 32 << 10
	for _, l := range d.opts.Limiters {
		if b := l.Burst(); b > 0 {
			size = min(size, b)
		}
	}
	return size
}

// attemptError classifies one failed request.
type attemptError struct {
	err        error
	retryable  bool
	retryAfter string
}

func (e *attemptError) Error() string { return e.err.Error() }
func (e *attemptError) Unwrap() error { return e.err }

func fatal(err error) *attemptError     { return &attemptError{err: err} }
func retryable(err error) *attemptError { return &attemptError{err: err, retryable: true} }

func statusError(uri string, resp *http.Response) *attemptError {
	code := resp.StatusCode
	retry := code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
	return &attemptError{
		err:        fmt.Errorf("GET %s: HTTP %s", uri, resp.Status),
		retryable:  retry,
		retryAfter: resp.Header.Get("Retry-After"),
	}
}

// maxRetryAfter bounds how long a server's Retry-After can park a request.
const maxRetryAfter = 5 * time.Minute

// sleepBeforeRetry honours Retry-After up to maxRetryAfter, otherwise waits an
// exponential backoff with full jitter, capped at maxBackoff.
func sleepBeforeRetry(ctx context.Context, attempt int, retryAfter string, base, maxBackoff time.Duration) error {
	delay := parseRetryAfter(retryAfter, time.Now())
	if delay >= 0 {
		delay = min(delay, maxRetryAfter)
	} else {
		ceiling := base
		for i := 0; i < attempt && ceiling < maxBackoff; i++ {
			ceiling *= 2
		}
		delay = fullJitter(min(ceiling, maxBackoff))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return -1
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, int64(maxRetryAfter/time.Second))) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		return max(t.Sub(now), 0)
	}
	return -1
}

func fullJitter(ceiling time.Duration) time.Duration {
	if ceiling <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return ceiling / 2
	}
	return time.Duration(binary.LittleEndian.Uint64(b[:]) % uint64(ceiling+1))
}

// stallTimer fires when a request receives nothing for its timeout. It is
// armed once the transport hands over a connection, so waiting for a pooled
// connection never counts.
type stallTimer struct {
	timer   *time.Timer
	timeout time.Duration
}

func newStallTimer(timeout time.Duration, fire func()) *stallTimer {
	t := time.AfterFunc(timeout, fire)
	t.Stop()
	return &stallTimer{timer: t, timeout: timeout}
}

func (s *stallTimer) Stop()                 { s.timer.Stop() }
func (s *stallTimer) Reset(d time.Duration) { s.timer.Reset(d) }

func (s *stallTimer) trace(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { s.timer.Reset(s.timeout) },
	})
}
