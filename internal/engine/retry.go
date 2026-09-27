package engine

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"time"
)

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

func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
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
		const maxSeconds = int64((1<<63 - 1) / int64(time.Second))
		if seconds > maxSeconds {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if t.Before(now) {
			return 0
		}
		return t.Sub(now)
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

// stallTimer fires when a request receives nothing for its timeout. It starts
// only once the transport hands over a connection, so waiting for a pooled
// connection slot never counts, and callers stop it around disk writes.
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

// trace arms the timer when the request gets its connection.
func (s *stallTimer) trace(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { s.timer.Reset(s.timeout) },
	})
}
