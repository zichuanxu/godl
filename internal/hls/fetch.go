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
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxInMemory bounds a playlist or key, the only resources held in memory;
// segments stream to disk.
const maxInMemory = 16 << 20

var errStalled = errors.New("connection stalled")

// sink receives one response body; reset rewinds it before a retry.
type sink interface {
	io.Writer
	reset() error
}

type memSink struct{ bytes.Buffer }

func (m *memSink) reset() error { m.Buffer.Reset(); return nil }

type fileSink struct{ f *os.File }

func (s fileSink) Write(p []byte) (int, error) { return s.f.Write(p) }

func (s fileSink) reset() error {
	if err := s.f.Truncate(0); err != nil {
		return err
	}
	_, err := s.f.Seek(0, io.SeekStart)
	return err
}

// get fetches a small resource (a playlist or key) into memory. It returns
// the body and the URL after redirects.
func (d *downloader) get(ctx context.Context, uri string) ([]byte, *url.URL, error) {
	var buf memSink
	final, n, err := d.fetch(ctx, uri, 0, -1, false, maxInMemory, &buf)
	if err != nil {
		return nil, nil, err
	}
	return buf.Bytes()[:n], final, nil
}

// fetch streams uri, or its byte range [off, off+n) when n >= 0, into w,
// retrying transient failures, and returns the final URL and the byte count.
// limit bounds the body (-1 for none). When counted, received bytes feed the
// progress tracker.
func (d *downloader) fetch(ctx context.Context, uri string, off, n int64, counted bool, limit int64, w sink) (*url.URL, int64, error) {
	var last *attemptError
	for attempt := 0; attempt < d.opts.MaxAttempts; attempt++ {
		if last != nil {
			if err := sleepBeforeRetry(ctx, attempt-1, last.retryAfter, d.opts.BaseBackoff, d.opts.MaxBackoff); err != nil {
				return nil, 0, err
			}
			if err := w.reset(); err != nil {
				return nil, 0, err
			}
		}
		var got int64
		final, written, aerr := d.getOnce(ctx, uri, off, n, limit, w, func(k int) {
			if counted {
				got += int64(k)
				d.prog.add(int64(k))
			}
		})
		if aerr == nil {
			return final, written, nil
		}
		d.prog.add(-got)
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		if last = aerr; !aerr.retryable {
			break
		}
	}
	return nil, 0, last
}

// portableHeaders may go to a host other than the playlist's. Playlists name
// segment and key URIs on any host, and credentials, cookies, and API keys
// configured for the playlist's host must not follow them there.
var portableHeaders = map[string]bool{"User-Agent": true, "Accept": true, "Accept-Language": true, "Referer": true}

func (d *downloader) getOnce(ctx context.Context, uri string, off, n, limit int64, w sink, onRead func(int)) (*url.URL, int64, *attemptError) {
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
		return nil, 0, fatal(err)
	}
	sameHost := strings.EqualFold(req.URL.Hostname(), d.origin)
	for k, v := range d.opts.Headers {
		if sameHost || portableHeaders[http.CanonicalHeaderKey(k)] {
			req.Header[k] = slices.Clone(v)
		}
	}
	req.Header.Set("User-Agent", d.opts.UserAgent)
	req.Header.Set("Accept-Encoding", "identity")
	if n >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+n-1))
	}
	resp, err := d.opts.Client.Do(req)
	if err != nil {
		return nil, 0, interrupted(err)
	}
	defer resp.Body.Close()
	if enc := strings.TrimSpace(resp.Header.Get("Content-Encoding")); enc != "" && !strings.EqualFold(enc, "identity") {
		return nil, 0, fatal(fmt.Errorf("GET %s: unsupported Content-Encoding %q", uri, enc))
	}
	skip, want := int64(0), n
	switch {
	case resp.StatusCode == http.StatusPartialContent && n >= 0:
		if start, ok := rangeStart(resp.Header.Get("Content-Range")); !ok || start != off {
			return nil, 0, fatal(fmt.Errorf("GET %s: Content-Range %q does not start at %d", uri, resp.Header.Get("Content-Range"), off))
		}
	case resp.StatusCode == http.StatusOK && n >= 0:
		skip = off // the server ignored Range; cut the sub-range out ourselves
	case resp.StatusCode == http.StatusOK:
		want = resp.ContentLength
	default:
		return nil, 0, statusError(uri, resp)
	}
	if limit >= 0 && want > limit {
		return nil, 0, fatal(fmt.Errorf("GET %s: %d bytes exceeds the %d-byte limit", uri, want, limit))
	}

	var written int64
	chunk := make([]byte, d.readSize())
	for want < 0 || written < want {
		k, readErr := resp.Body.Read(chunk)
		if k > 0 {
			stall.Stop() // waiting on a rate limiter is not a stall
			for _, l := range d.opts.Limiters {
				if err := l.WaitN(reqCtx, k); err != nil {
					if reqCtx.Err() != nil {
						return nil, 0, interrupted(err)
					}
					return nil, 0, fatal(fmt.Errorf("rate limiter: %w", err))
				}
			}
			stall.Reset(d.opts.StallTimeout)
			keep := chunk[min(int64(k), skip):k]
			skip -= int64(k - len(keep))
			if want >= 0 {
				keep = keep[:min(int64(len(keep)), want-written)]
			}
			if _, err := w.Write(keep); err != nil {
				return nil, 0, fatal(fmt.Errorf("GET %s: write: %w", uri, err))
			}
			written += int64(len(keep))
			onRead(len(keep))
			if limit >= 0 && written > limit {
				return nil, 0, fatal(fmt.Errorf("GET %s: body exceeds the %d-byte limit", uri, limit))
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, 0, interrupted(readErr)
		}
	}
	if want >= 0 && written != want {
		return nil, 0, retryable(fmt.Errorf("GET %s: short response: got %d bytes, want %d", uri, written, want))
	}
	return resp.Request.URL, written, nil
}

// rangeStart reads the first byte position of "bytes a-b/c".
func rangeStart(header string) (int64, bool) {
	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes ")
	if !ok {
		return 0, false
	}
	first, _, ok := strings.Cut(spec, "-")
	start, err := strconv.ParseInt(first, 10, 64)
	return start, ok && err == nil
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
