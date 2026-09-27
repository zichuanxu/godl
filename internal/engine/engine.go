// Package engine downloads HTTP resources with dynamic work-stealing
// segmentation and byte-granular resume, as specified in DESIGN.md section 3.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zichuanxu/godl/internal/filelock"
)

var (
	ErrDestinationExists = errors.New("destination already exists")
	ErrSourceChanged     = errors.New("remote resource changed while downloading")
	ErrRangeUnsupported  = errors.New("remote server stopped honoring range requests")
	ErrLocked            = errors.New("destination is locked by another download")
	ErrStalled           = errors.New("connection stalled")
	ErrInsufficientSpace = errors.New("not enough free disk space")
)

const (
	maxConnections = 32
	bufferSize     = 256 << 10
)

type Config struct {
	// Connections per download: default 8, at most 32.
	Connections int
	// MinSplitSize is the smallest range handed to a connection: default
	// 1 MiB, at least 256 KiB so a split never cuts through a buffered write.
	MinSplitSize int64
	// MaxAttempts bounds consecutive failed requests without progress.
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// StallTimeout fails a request that receives no bytes for this long.
	// Time spent blocked outside the network read never counts.
	StallTimeout       time.Duration
	CheckpointInterval time.Duration
	ProgressInterval   time.Duration

	Headers   http.Header
	UserAgent string
	// Checksum is an optional "algo:hex" digest verified before commit.
	Checksum string
	// ResumeKey identifies the file across expiring signed URLs; the URL is
	// used when it is empty.
	ResumeKey string
	Overwrite bool
	// FileMode is applied after the complete file has been verified.
	FileMode os.FileMode

	// ProbeClient negotiates protocols normally; SegmentClient forces
	// HTTP/1.1 so every connection gets its own TCP congestion window.
	ProbeClient   *http.Client
	SegmentClient *http.Client
}

type Progress struct {
	Completed int64
	Total     int64 // -1 when unknown
}

type Engine struct {
	cfg Config
	sum *checksum
}

func New(cfg Config) (*Engine, error) {
	defaults := []struct {
		value *time.Duration
		def   time.Duration
		name  string
	}{
		{&cfg.BaseBackoff, 300 * time.Millisecond, "base backoff"},
		{&cfg.MaxBackoff, 10 * time.Second, "max backoff"},
		{&cfg.StallTimeout, 30 * time.Second, "stall timeout"},
		{&cfg.CheckpointInterval, 2 * time.Second, "checkpoint interval"},
		{&cfg.ProgressInterval, 250 * time.Millisecond, "progress interval"},
	}
	for _, d := range defaults {
		if *d.value == 0 {
			*d.value = d.def
		}
		if *d.value < 0 {
			return nil, fmt.Errorf("%s cannot be negative", d.name)
		}
	}
	if cfg.MaxBackoff < cfg.BaseBackoff {
		return nil, errors.New("max backoff is below base backoff")
	}
	if cfg.Connections == 0 {
		cfg.Connections = 8
	}
	if cfg.Connections < 1 || cfg.Connections > maxConnections {
		return nil, fmt.Errorf("connections must be in [1, %d], got %d", maxConnections, cfg.Connections)
	}
	if cfg.MinSplitSize == 0 {
		cfg.MinSplitSize = 1 << 20
	}
	if cfg.MinSplitSize < bufferSize {
		return nil, fmt.Errorf("minimum split size must be at least %d bytes", bufferSize)
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.MaxAttempts < 1 {
		return nil, errors.New("max attempts must be at least 1")
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "godl/1.0"
	}
	if cfg.FileMode == 0 {
		cfg.FileMode = 0o644
	}
	cfg.Headers = cfg.Headers.Clone()
	if cfg.Headers == nil {
		cfg.Headers = make(http.Header)
	}
	if cfg.ProbeClient == nil {
		cfg.ProbeClient = newProbeClient()
	}
	if cfg.SegmentClient == nil {
		// ponytail: one pool for every download; per-host caps arrive in M2.
		cfg.SegmentClient = newSegmentClient(2 * maxConnections)
	}
	sum, err := parseChecksum(cfg.Checksum)
	if err != nil {
		return nil, err
	}
	return &Engine{cfg: cfg, sum: sum}, nil
}

// Download writes dest+".part", keeps resume state in dest+".part.meta", and
// atomically renames the verified file to dest. Partial state survives
// failures and cancellation so a later call resumes.
func (e *Engine) Download(ctx context.Context, rawURL, dest string, onProgress func(Progress)) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	if err := validateHTTPURL(rawURL); err != nil {
		return err
	}
	if strings.TrimSpace(dest) == "" {
		return errors.New("empty destination path")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	release, err := filelock.Acquire(dest + ".lock")
	if errors.Is(err, filelock.ErrLocked) {
		return fmt.Errorf("%w: %s", ErrLocked, dest)
	}
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	if !e.cfg.Overwrite {
		if _, err := os.Lstat(dest); err == nil {
			return fmt.Errorf("%w: %s", ErrDestinationExists, dest)
		}
	}

	job := &job{
		engine: e, url: rawURL, dest: dest,
		part: dest + ".part", meta: dest + ".part.meta",
		identity: e.cfg.ResumeKey,
		progress: startProgress(onProgress, e.cfg.ProgressInterval),
		segments: e.cfg.SegmentClient, connections: e.cfg.Connections,
	}
	if job.identity == "" {
		job.identity = rawURL
	}
	defer job.progress.finish()

	// One automatic restart covers a representation change between the probe
	// and a later request; one more covers a server that refuses HTTP/1.1.
	for attempt := 0; attempt < 3; attempt++ {
		info, err := e.probe(ctx, rawURL)
		if err != nil {
			return err
		}
		if info.ranges && info.size > 0 {
			err = job.ranged(ctx, info)
		} else {
			err = job.single(ctx, info)
		}
		switch {
		case err == nil:
			return job.commit()
		case errors.Is(err, errNeedsHTTP2) && job.segments != e.cfg.ProbeClient:
			job.segments, job.connections = e.cfg.ProbeClient, 1
		case attempt == 0 && (errors.Is(err, ErrSourceChanged) || errors.Is(err, ErrRangeUnsupported)):
			// Re-probe. Written bytes came from validated responses, so openPart
			// keeps them when the validators still match and discards them
			// otherwise.
		default:
			return err
		}
	}
	return ErrSourceChanged
}

// job is the state of one Download call.
type job struct {
	engine      *Engine
	url, dest   string
	part, meta  string
	identity    string
	progress    *progressReporter
	segments    *http.Client
	connections int
}

func (j *job) discard() {
	_ = os.Remove(j.part)
	_ = os.Remove(j.meta)
}

// commit verifies the closed .part file and renames it into place.
func (j *job) commit() error {
	if err := j.engine.sum.verify(j.part); err != nil {
		if errors.Is(err, ErrChecksumMismatch) {
			j.discard()
		}
		return err
	}
	if err := os.Chmod(j.part, j.engine.cfg.FileMode); err != nil {
		return fmt.Errorf("chmod completed file: %w", err)
	}
	if err := j.place(); err != nil {
		return err
	}
	syncDir(filepath.Dir(j.dest))
	_ = os.Remove(j.meta) // the destination is committed; stale metadata is harmless
	return nil
}

// place moves the completed file to dest. Without Overwrite it hard-links,
// which fails atomically if dest appeared meanwhile; file systems without
// hard links (FAT, exFAT) fall back to check-then-rename.
func (j *job) place() error {
	if !j.engine.cfg.Overwrite {
		err := os.Link(j.part, j.dest)
		if err == nil {
			_ = os.Remove(j.part)
			return nil
		}
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrDestinationExists, j.dest)
		}
		if _, err := os.Lstat(j.dest); err == nil {
			return fmt.Errorf("%w: %s", ErrDestinationExists, j.dest)
		}
	}
	if err := os.Rename(j.part, j.dest); err != nil {
		return fmt.Errorf("rename completed file: %w", err)
	}
	return nil
}

type probeInfo struct {
	size         int64 // -1 when unknown
	ranges       bool
	etag         string // strong ETag only
	lastModified string
}

func (e *Engine) probe(ctx context.Context, rawURL string) (probeInfo, error) {
	var lastErr error
	for attempt := 0; attempt < e.cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			var retryAfter string
			var aerr *attemptError
			if errors.As(lastErr, &aerr) {
				retryAfter = aerr.retryAfter
			}
			if err := sleepBeforeRetry(ctx, attempt-1, retryAfter, e.cfg.BaseBackoff, e.cfg.MaxBackoff); err != nil {
				return probeInfo{}, err
			}
		}
		info, aerr := e.probeOnce(ctx, rawURL)
		if aerr == nil {
			return info, nil
		}
		if ctx.Err() != nil {
			return probeInfo{}, ctx.Err()
		}
		lastErr = aerr
		if !aerr.retryable {
			break
		}
	}
	return probeInfo{}, lastErr
}

func (e *Engine) probeOnce(ctx context.Context, rawURL string) (probeInfo, *attemptError) {
	reqCtx, cancel := context.WithTimeout(ctx, e.cfg.StallTimeout)
	defer cancel()
	req, err := e.newRequest(reqCtx, rawURL)
	if err != nil {
		return probeInfo{}, fatal(err)
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := e.cfg.ProbeClient.Do(req)
	if err != nil {
		return probeInfo{}, retryable(fmt.Errorf("probe request: %w", err))
	}
	defer resp.Body.Close()
	if err := identityEncoding(resp); err != nil {
		return probeInfo{}, fatal(err)
	}
	info := probeInfo{etag: strongETag(resp.Header.Get("ETag")), lastModified: resp.Header.Get("Last-Modified")}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, end, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != 0 || end != 0 {
			return probeInfo{}, fatal(fmt.Errorf("invalid probe Content-Range %q", resp.Header.Get("Content-Range")))
		}
		if _, err := io.CopyN(io.Discard, resp.Body, 1); err != nil {
			return probeInfo{}, retryable(fmt.Errorf("read probe body: %w", err))
		}
		info.size, info.ranges = total, true
		return info, nil
	case http.StatusOK:
		// Range ignored. The body is not drained; closing drops the connection.
		info.size = resp.ContentLength
		return info, nil
	case http.StatusRequestedRangeNotSatisfiable:
		if total, ok := parseUnsatisfiedContentRange(resp.Header.Get("Content-Range")); ok && total == 0 {
			info.size = 0
			return info, nil
		}
		return probeInfo{}, fatal(fmt.Errorf("probe returned 416 with Content-Range %q", resp.Header.Get("Content-Range")))
	default:
		return probeInfo{}, statusError("probe", resp)
	}
}

// newRequest builds a GET carrying the caller's headers, minus the ones the
// engine owns: letting callers inject them would break range validation.
func (e *Engine) newRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header = e.cfg.Headers.Clone()
	for _, name := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		req.Header.Del(name)
	}
	req.Header.Set("Accept-Encoding", "identity")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", e.cfg.UserAgent)
	}
	return req, nil
}

func validateHTTPURL(raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("URL has no host")
	}
	return nil
}

func identityEncoding(resp *http.Response) error {
	if enc := strings.TrimSpace(resp.Header.Get("Content-Encoding")); enc != "" && !strings.EqualFold(enc, "identity") {
		return fmt.Errorf("server returned unsupported Content-Encoding %q", enc)
	}
	return nil
}

func statusError(what string, resp *http.Response) *attemptError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	msg := fmt.Sprintf("%s: HTTP %s", what, resp.Status)
	if text := strings.TrimSpace(string(body)); text != "" {
		msg += ": " + text
	}
	return &attemptError{err: errors.New(msg), retryable: retryableStatus(resp.StatusCode), retryAfter: resp.Header.Get("Retry-After")}
}

// progressReporter publishes byte counts at most once per interval from its
// own goroutine, so the download path only touches atomics.
type progressReporter struct {
	completed atomic.Int64
	total     atomic.Int64
	callback  func(Progress)
	stop      chan struct{}
	done      sync.WaitGroup
}

func startProgress(callback func(Progress), interval time.Duration) *progressReporter {
	p := &progressReporter{callback: callback, stop: make(chan struct{})}
	p.total.Store(-1)
	if callback == nil {
		return p
	}
	p.done.Add(1)
	go func() {
		defer p.done.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		last := Progress{Completed: -1}
		for {
			select {
			case <-ticker.C:
				if now := p.snapshot(); now != last {
					p.callback(now)
					last = now
				}
			case <-p.stop:
				return
			}
		}
	}()
	return p
}

func (p *progressReporter) snapshot() Progress {
	return Progress{Completed: p.completed.Load(), Total: p.total.Load()}
}

// finish stops the ticker and reports the final counts once.
func (p *progressReporter) finish() {
	if p.callback == nil {
		return
	}
	close(p.stop)
	p.done.Wait()
	p.callback(p.snapshot())
}
