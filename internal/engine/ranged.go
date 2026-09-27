package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errNeedsHTTP2 = errors.New("server refused HTTP/1.1")
	// errSlow aborts a request so the range reconnects on a fresh connection.
	errSlow = errors.New("connection replaced for being slow")
)

const (
	freeSpaceMargin = 64 << 20
	// A connection slower than slowFraction of the average, sampled for at
	// least slowMinAge, is replaced.
	slowFraction = 0.3
	slowMinAge   = 3 * time.Second
)

// rangedRun downloads one file over parallel HTTP range requests.
type rangedRun struct {
	job   *job
	info  probeInfo
	want  checkpoint
	file  *os.File
	sched *scheduler
	// noIfRange is set when the server answers a Last-Modified If-Range with
	// a full body; ranges are then validated by Content-Range alone.
	noIfRange atomic.Bool
	// alive counts workers still running.
	alive atomic.Int32
	// began and baseBytes give the run's average per-connection speed.
	began     time.Time
	baseBytes int64
}

func (j *job) ranged(ctx context.Context, info probeInfo) error {
	e := j.engine
	want := checkpoint{Version: checkpointVersion, Identity: j.identity, Size: info.size, ETag: info.etag, LastModified: info.lastModified}
	remaining, err := j.openPart(want)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(j.part, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open partial file: %w", err)
	}
	defer f.Close()
	j.progress.total.Store(info.size)
	j.progress.completed.Store(info.size - totalSize(remaining))

	r := &rangedRun{
		job: j, info: info, want: want, file: f,
		sched: newScheduler(remaining, e.cfg.MinSplitSize, j.connections),
		began: time.Now(), baseBytes: j.progress.completed.Load(),
	}
	workCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var workers sync.WaitGroup
	r.alive.Store(int32(j.connections))
	for range j.connections {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.worker(workCtx, cancel)
		}()
	}
	stop := make(chan struct{})
	var background sync.WaitGroup
	background.Add(2)
	go func() {
		defer background.Done()
		r.every(stop, e.cfg.CheckpointInterval, func() {
			if err := r.checkpoint(); err != nil {
				cancel(err)
			}
		})
	}()
	go func() {
		defer background.Done()
		r.every(stop, time.Second, r.replaceSlow)
	}()
	workers.Wait()
	close(stop)
	background.Wait()

	// Final checkpoint on success, failure, and cancellation alike.
	cpErr := r.checkpoint()
	if cause := context.Cause(workCtx); cause != nil && !errors.Is(cause, context.Canceled) {
		return errors.Join(cause, cpErr)
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, cpErr)
	}
	if cpErr != nil {
		return cpErr
	}
	if left := r.sched.remaining(); len(left) > 0 {
		return fmt.Errorf("download stopped with %d bytes left", totalSize(left))
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync partial file: %w", err)
	}
	return f.Close()
}

// openPart resumes a compatible checkpoint or creates a preallocated .part
// file, returning the byte ranges still to download.
func (j *job) openPart(want checkpoint) ([]interval, error) {
	if cp, err := readCheckpoint(j.meta); err == nil && cp.resumable(want) {
		if fi, err := os.Lstat(j.part); err == nil && fi.Mode().IsRegular() && fi.Size() == want.Size {
			return cp.Remaining, nil
		}
	}
	j.discard()
	if free, err := freeSpace(filepath.Dir(j.part)); err == nil && free < want.Size+freeSpaceMargin {
		return nil, fmt.Errorf("%w: need %d bytes plus margin, %d available", ErrInsufficientSpace, want.Size, free)
	}
	f, err := os.OpenFile(j.part, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create partial file: %w", err)
	}
	if err := preallocate(f, want.Size); err != nil {
		_ = f.Close()
		j.discard()
		return nil, err
	}
	if err := f.Close(); err != nil {
		j.discard()
		return nil, fmt.Errorf("close new partial file: %w", err)
	}
	remaining := []interval{{0, want.Size}}
	if want.ETag != "" || want.LastModified != "" {
		want.Remaining = remaining
		if err := writeCheckpoint(j.meta, want); err != nil {
			j.discard()
			return nil, err
		}
	}
	return remaining, nil
}

// checkpoint persists progress. The snapshot is taken before the data sync:
// every byte it omits was written before the snapshot, so the sync makes the
// claim true before the metadata is replaced.
func (r *rangedRun) checkpoint() error {
	if r.want.ETag == "" && r.want.LastModified == "" {
		return nil // not resumable across sessions anyway (DESIGN.md 3.3)
	}
	state := r.want
	state.Remaining = r.sched.remaining()
	if err := r.file.Sync(); err != nil {
		return fmt.Errorf("checkpoint file sync: %w", err)
	}
	return writeCheckpoint(r.job.meta, state)
}

func (r *rangedRun) every(stop <-chan struct{}, interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			fn()
		case <-stop:
			return
		}
	}
}

// replaceSlow aborts requests far below the average speed so the range
// reconnects on a fresh connection; idle connections also steal half of it.
// ponytail: all-busy slow ranges only reconnect (at most every slowMinAge);
// spawning an extra connection to take the upper half is the upgrade path.
func (r *rangedRun) replaceSlow() {
	now := time.Now()
	type sample struct {
		seg   *segment
		speed float64
	}
	var samples []sample
	var sum float64
	for _, seg := range r.sched.segments() {
		age := now.Sub(time.Unix(0, seg.started.Load()))
		if seg.started.Load() == 0 || age < slowMinAge {
			continue
		}
		speed := float64(seg.bytes.Load()) / age.Seconds()
		samples = append(samples, sample{seg, speed})
		sum += speed
	}
	if len(samples) == 0 {
		return
	}
	// Compare against the live average and against the run's history, so a
	// slow tail range is still caught after the fast connections finished.
	reference := float64(r.job.progress.completed.Load()-r.baseBytes) / now.Sub(r.began).Seconds() / float64(r.job.connections)
	if len(samples) >= 2 {
		reference = max(reference, sum/float64(len(samples)))
	}
	for _, s := range samples {
		// Reconnecting costs one request; it pays off when this connection
		// would need longer than slowMinAge for what it has left.
		if s.speed < reference*slowFraction && float64(s.seg.remaining()) > s.speed*slowMinAge.Seconds() {
			if abort := s.seg.abort.Load(); abort != nil {
				(*abort)()
			}
		}
	}
}

func (r *rangedRun) worker(ctx context.Context, fail context.CancelCauseFunc) {
	e := r.job.engine
	defer r.alive.Add(-1)
	buf := make([]byte, bufferSize)
	failures := 0
	for ctx.Err() == nil {
		seg := r.sched.next()
		if seg == nil {
			return
		}
		progressed, err := r.fetch(ctx, seg, buf)
		r.sched.release(seg)
		if err == nil {
			failures = 0
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errSlow) {
			continue
		}
		var aerr *attemptError
		if !errors.As(err, &aerr) || !aerr.retryable {
			fail(err)
			return
		}
		if progressed {
			failures = 0
		}
		failures++
		if failures >= e.cfg.MaxAttempts {
			// A server capping connections per client rejects the extra ones
			// indefinitely; that connection retires and the others take its
			// range. Only the last one standing fails the download.
			if r.alive.Load() > 1 {
				return
			}
			fail(err)
			return
		}
		if err := sleepBeforeRetry(ctx, failures-1, aerr.retryAfter, e.cfg.BaseBackoff, e.cfg.MaxBackoff); err != nil {
			return
		}
	}
}

// fetch downloads seg with one request until the segment is done, the
// request fails, or a steal shrinks the segment to what was already read.
func (r *rangedRun) fetch(ctx context.Context, seg *segment, buf []byte) (progressed bool, err error) {
	e := r.job.engine
	seg.mu.Lock()
	start, end := seg.cursor, seg.end
	seg.mu.Unlock()
	if start >= end {
		return false, nil
	}

	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := newStallTimer(e.cfg.StallTimeout, func() { cancel(ErrStalled) })
	defer stall.Stop()
	reqCtx = stall.trace(reqCtx)
	abort := func() { cancel(errSlow) }
	seg.abort.Store(&abort)
	defer seg.abort.Store(nil)
	seg.resetSample(time.Now())

	// interrupted maps a failure to its cause: a stall, a slow-connection
	// replacement, cancellation, or a plain network error.
	interrupted := func(err error) error {
		switch cause := context.Cause(reqCtx); {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(cause, errSlow):
			return errSlow
		case errors.Is(cause, ErrStalled):
			return retryable(fmt.Errorf("range %d-%d: %w", start, end-1, ErrStalled))
		}
		return retryable(fmt.Errorf("range %d-%d: %w", start, end-1, err))
	}

	req, err := e.newRequest(reqCtx, r.job.url)
	if err != nil {
		return false, fatal(err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
	ifRange := r.info.etag
	if ifRange == "" && !r.noIfRange.Load() {
		ifRange = r.info.lastModified
	}
	if ifRange != "" {
		req.Header.Set("If-Range", ifRange)
	}
	resp, err := r.job.segments.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "no application protocol") {
			return false, fatal(errNeedsHTTP2)
		}
		return false, interrupted(err)
	}
	defer resp.Body.Close()
	if aerr := r.validate(resp, start, end, ifRange); aerr != nil {
		return false, aerr
	}

	for {
		cursor, n := seg.window(int64(len(buf)))
		if n <= 0 {
			return progressed, nil // done, or the rest was stolen
		}
		// Write whatever each read returns: progress and checkpoints then move
		// with the data even on a slow connection, and a write never exceeds
		// the buffer, which keeps steals from cutting through it.
		m, readErr := resp.Body.Read(buf[:n])
		if m > 0 {
			stall.Stop() // a slow disk is not a stalled connection
			if _, err := r.file.WriteAt(buf[:m], cursor); err != nil {
				return progressed, fatal(fmt.Errorf("write partial file: %w", err))
			}
			stall.Reset(e.cfg.StallTimeout)
			seg.advance(int64(m))
			seg.bytes.Add(int64(m))
			r.job.progress.completed.Add(int64(m))
			progressed = true
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) && int64(m) == n {
				continue // the next window reports completion
			}
			if errors.Is(readErr, io.EOF) {
				readErr = io.ErrUnexpectedEOF
			}
			return progressed, interrupted(readErr)
		}
	}
}

// validate checks a range response against the probed representation.
func (r *rangedRun) validate(resp *http.Response, start, end int64, ifRange string) *attemptError {
	if err := identityEncoding(resp); err != nil {
		return fatal(err)
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		if r.changed(resp) || (ifRange != "" && ifRange == r.info.lastModified && resp.Header.Get("Last-Modified") == "") {
			return fatal(ErrSourceChanged)
		}
		if ifRange != "" && ifRange == r.info.lastModified && resp.Header.Get("Last-Modified") == r.info.lastModified {
			// Same representation, yet a full body: the server does not honour
			// date validators in If-Range.
			r.noIfRange.Store(true)
			return retryable(errors.New("server ignored If-Range with a date; retrying without it"))
		}
		return fatal(ErrRangeUnsupported)
	case http.StatusPreconditionFailed, http.StatusRequestedRangeNotSatisfiable:
		return fatal(ErrSourceChanged)
	default:
		return statusError(fmt.Sprintf("range %d-%d", start, end-1), resp)
	}
	if r.changed(resp) {
		return fatal(ErrSourceChanged)
	}
	gotStart, gotEnd, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || gotStart != start || gotEnd != end-1 || total != r.info.size {
		return fatal(fmt.Errorf("%w: unexpected Content-Range %q for bytes %d-%d", ErrSourceChanged, resp.Header.Get("Content-Range"), start, end-1))
	}
	if resp.ContentLength >= 0 && resp.ContentLength != end-start {
		return fatal(fmt.Errorf("range %d-%d has Content-Length %d", start, end-1, resp.ContentLength))
	}
	return nil
}

// changed reports a response whose validators differ from the probe's.
func (r *rangedRun) changed(resp *http.Response) bool {
	if r.info.etag != "" {
		return strongETag(resp.Header.Get("ETag")) != r.info.etag
	}
	lm := resp.Header.Get("Last-Modified")
	return r.info.lastModified != "" && lm != "" && lm != r.info.lastModified
}
