// Package downloader implements a resumable, concurrent HTTP file downloader.
package downloader

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrDestinationExists = errors.New("destination already exists")
	ErrSourceChanged     = errors.New("remote resource changed while downloading")
	ErrRangeUnsupported  = errors.New("remote server stopped honoring range requests")
	ErrLocked            = errors.New("destination is locked by another downloader")
)

// Config is immutable after New returns. A Downloader may be reused concurrently
// for different destination files.
type Config struct {
	Client *http.Client

	Workers            int
	ChunkSize          int64
	MinParallelSize    int64
	MaxAttempts        int
	BaseBackoff        time.Duration
	MaxBackoff         time.Duration
	PartTimeout        time.Duration
	CheckpointInterval time.Duration
	ProgressInterval   time.Duration

	Headers   http.Header
	UserAgent string

	// ExpectedSHA256 is an optional lowercase or uppercase 64-character hex digest.
	ExpectedSHA256 string
	// ResumeKey can be stable across expiring/signed URLs. If empty, the URL is used.
	ResumeKey string
	// Overwrite controls whether an existing final destination may be replaced.
	Overwrite bool
	// FileMode is applied only after the complete file has been verified.
	FileMode os.FileMode
}

type Downloader struct {
	cfg Config
}

type Progress struct {
	Completed int64
	Total     int64
	Percent   float64
}

type probeInfo struct {
	Size           int64
	RangeSupported bool
	StrongETag     string
}

type resumeState struct {
	Version   int    `json:"version"`
	Identity  string `json:"identity"`
	Size      int64  `json:"size"`
	ETag      string `json:"etag"`
	ChunkSize int64  `json:"chunk_size"`
	Chunks    int    `json:"chunks"`
	Completed []byte `json:"completed"`
}

type stateTracker struct {
	mu sync.Mutex
	st resumeState
}

type progressTracker struct {
	total atomic.Int64
	done  atomic.Int64

	mu       sync.Mutex
	last     time.Time
	interval time.Duration
	callback func(Progress)
}

type attemptError struct {
	err        error
	retryable  bool
	retryAfter string
}

func (e *attemptError) Error() string { return e.err.Error() }
func (e *attemptError) Unwrap() error { return e.err }

// New validates cfg and fills production-oriented defaults.
func New(cfg Config) (*Downloader, error) {
	if cfg.Workers == 0 {
		cfg.Workers = 8
	}
	if cfg.Workers < 1 || cfg.Workers > 256 {
		return nil, fmt.Errorf("workers must be in [1, 256], got %d", cfg.Workers)
	}
	if cfg.ChunkSize == 0 {
		cfg.ChunkSize = 8 << 20 // 8 MiB
	}
	if cfg.ChunkSize < 64<<10 {
		return nil, fmt.Errorf("chunk size must be at least 64 KiB")
	}
	if cfg.MinParallelSize == 0 {
		cfg.MinParallelSize = 32 << 20 // 32 MiB
	}
	if cfg.MinParallelSize < 0 {
		return nil, fmt.Errorf("min parallel size cannot be negative")
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.MaxAttempts < 1 {
		return nil, fmt.Errorf("max attempts must be at least 1")
	}
	if cfg.BaseBackoff == 0 {
		cfg.BaseBackoff = 300 * time.Millisecond
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 10 * time.Second
	}
	if cfg.BaseBackoff < 0 || cfg.MaxBackoff < cfg.BaseBackoff {
		return nil, fmt.Errorf("invalid backoff configuration")
	}
	if cfg.PartTimeout == 0 {
		cfg.PartTimeout = 2 * time.Minute
	}
	if cfg.PartTimeout < 0 {
		return nil, fmt.Errorf("part timeout cannot be negative")
	}
	if cfg.CheckpointInterval == 0 {
		cfg.CheckpointInterval = 2 * time.Second
	}
	if cfg.CheckpointInterval < 0 {
		return nil, fmt.Errorf("checkpoint interval cannot be negative")
	}
	if cfg.ProgressInterval == 0 {
		cfg.ProgressInterval = 250 * time.Millisecond
	}
	if cfg.ProgressInterval < 0 {
		return nil, fmt.Errorf("progress interval cannot be negative")
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "godl/1.0"
	}
	if cfg.FileMode == 0 {
		cfg.FileMode = 0o644
	}
	if cfg.ExpectedSHA256 != "" {
		if _, err := parseSHA256(cfg.ExpectedSHA256); err != nil {
			return nil, err
		}
		cfg.ExpectedSHA256 = strings.ToLower(cfg.ExpectedSHA256)
	}
	if cfg.Headers == nil {
		cfg.Headers = make(http.Header)
	} else {
		cfg.Headers = cfg.Headers.Clone()
	}
	if cfg.Client == nil {
		cfg.Client = defaultHTTPClient(cfg.Workers)
	}
	return &Downloader{cfg: cfg}, nil
}

// Download writes to dest+".part", persists dest+".part.meta", and renames
// the complete verified file to dest. Partial data is intentionally kept on
// ordinary failures so the next invocation can resume.
func (d *Downloader) Download(ctx context.Context, rawURL, dest string, onProgress func(Progress)) error {
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

	unlock, err := acquireLock(dest + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	if !d.cfg.Overwrite {
		if _, err := os.Lstat(dest); err == nil {
			return fmt.Errorf("%w: %s", ErrDestinationExists, dest)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat destination: %w", err)
		}
	}

	partPath := dest + ".part"
	metaPath := dest + ".part.meta"

	// One automatic restart handles a representation change observed between
	// the probe and one of the range requests.
	for generation := 0; generation < 2; generation++ {
		info, err := d.probe(ctx, rawURL)
		if err != nil {
			return err
		}

		tracker := newProgressTracker(info.Size, d.cfg.ProgressInterval, onProgress)
		parallel := info.RangeSupported && info.StrongETag != "" && info.Size >= d.cfg.MinParallelSize && info.Size > 0

		if parallel {
			err = d.downloadParallel(ctx, rawURL, dest, partPath, metaPath, info, tracker)
		} else {
			err = d.downloadSingle(ctx, rawURL, dest, partPath, metaPath, info, tracker)
		}
		if err == nil {
			return nil
		}

		if generation == 0 && (errors.Is(err, ErrSourceChanged) || errors.Is(err, ErrRangeUnsupported)) {
			_ = os.Remove(partPath)
			_ = os.Remove(metaPath)
			continue
		}
		return err
	}
	return ErrSourceChanged
}

func defaultHTTPClient(workers int) *http.Client {
	perHost := workers + 4
	if perHost < 8 {
		perHost = 8
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	tr.MaxIdleConns = perHost * 2
	tr.MaxIdleConnsPerHost = perHost
	tr.MaxConnsPerHost = perHost
	tr.IdleConnTimeout = 90 * time.Second
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = 20 * time.Second
	tr.ExpectContinueTimeout = time.Second
	tr.DisableCompression = true
	return &http.Client{Transport: tr}
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

func (d *Downloader) newRequest(ctx context.Context, method, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header = d.cfg.Headers.Clone()
	// These headers are owned by the downloader. Letting callers inject them
	// would break range validation and could corrupt the output.
	for _, name := range []string{
		"Range", "If-Range", "If-Match", "If-None-Match",
		"If-Modified-Since", "If-Unmodified-Since",
	} {
		req.Header.Del(name)
	}
	req.Header.Set("Accept-Encoding", "identity")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", d.cfg.UserAgent)
	}
	return req, nil
}

func (d *Downloader) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if d.cfg.PartTimeout == 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, d.cfg.PartTimeout)
}

func (d *Downloader) probe(ctx context.Context, rawURL string) (probeInfo, error) {
	var lastErr error
	for attempt := 0; attempt < d.cfg.MaxAttempts; attempt++ {
		reqCtx, cancel := d.requestContext(ctx)
		req, err := d.newRequest(reqCtx, http.MethodGet, rawURL)
		if err != nil {
			cancel()
			return probeInfo{}, err
		}
		req.Header.Set("Range", "bytes=0-0")

		resp, err := d.cfg.Client.Do(req)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return probeInfo{}, ctx.Err()
			}
			lastErr = fmt.Errorf("probe request: %w", err)
			if attempt+1 < d.cfg.MaxAttempts {
				if err := d.sleepBeforeRetry(ctx, attempt, ""); err != nil {
					return probeInfo{}, err
				}
				continue
			}
			break
		}

		info, aerr := inspectProbeResponse(resp)
		cancel()
		if aerr == nil {
			return info, nil
		}
		lastErr = aerr.err
		if !aerr.retryable || attempt+1 >= d.cfg.MaxAttempts {
			break
		}
		if err := d.sleepBeforeRetry(ctx, attempt, aerr.retryAfter); err != nil {
			return probeInfo{}, err
		}
	}
	return probeInfo{}, lastErr
}

func inspectProbeResponse(resp *http.Response) (probeInfo, *attemptError) {
	defer resp.Body.Close()
	if enc := strings.TrimSpace(resp.Header.Get("Content-Encoding")); enc != "" && !strings.EqualFold(enc, "identity") {
		return probeInfo{}, &attemptError{err: fmt.Errorf("server returned unsupported Content-Encoding %q", enc)}
	}

	strongETag := strongETag(resp.Header.Get("ETag"))
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, end, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != 0 || end != 0 || total < 1 {
			return probeInfo{}, &attemptError{err: fmt.Errorf("invalid probe Content-Range %q", resp.Header.Get("Content-Range"))}
		}
		if resp.ContentLength >= 0 && resp.ContentLength != 1 {
			return probeInfo{}, &attemptError{err: fmt.Errorf("invalid probe Content-Length %d", resp.ContentLength)}
		}
		if _, err := io.CopyN(io.Discard, resp.Body, 1); err != nil {
			return probeInfo{}, &attemptError{err: fmt.Errorf("read probe body: %w", err), retryable: true}
		}
		if err := requireEOF(resp.Body); err != nil {
			return probeInfo{}, &attemptError{err: fmt.Errorf("finish probe body: %w", err), retryable: true}
		}
		return probeInfo{Size: total, RangeSupported: true, StrongETag: strongETag}, nil

	case http.StatusOK:
		// The server ignored Range. Do not drain a potentially huge body.
		return probeInfo{Size: resp.ContentLength, RangeSupported: false, StrongETag: strongETag}, nil

	case http.StatusRequestedRangeNotSatisfiable:
		total, ok := parseUnsatisfiedContentRange(resp.Header.Get("Content-Range"))
		if ok && total == 0 {
			return probeInfo{Size: 0, RangeSupported: false, StrongETag: strongETag}, nil
		}
		return probeInfo{}, &attemptError{err: fmt.Errorf("probe returned 416 with Content-Range %q", resp.Header.Get("Content-Range"))}

	default:
		body := readSmallBody(resp.Body)
		err := fmt.Errorf("probe: HTTP %s%s", resp.Status, bodySuffix(body))
		return probeInfo{}, &attemptError{
			err:        err,
			retryable:  retryableStatus(resp.StatusCode),
			retryAfter: resp.Header.Get("Retry-After"),
		}
	}
}

func (d *Downloader) downloadSingle(
	ctx context.Context,
	rawURL, dest, partPath, metaPath string,
	info probeInfo,
	tracker *progressTracker,
) error {
	_ = os.Remove(metaPath)

	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open partial file: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	var lastErr error
	for attempt := 0; attempt < d.cfg.MaxAttempts; attempt++ {
		if err := f.Truncate(0); err != nil {
			return fmt.Errorf("truncate partial file: %w", err)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek partial file: %w", err)
		}
		tracker.setCompleted(0, true)

		reqCtx, cancel := d.requestContext(ctx)
		req, err := d.newRequest(reqCtx, http.MethodGet, rawURL)
		if err != nil {
			cancel()
			return err
		}
		if info.StrongETag != "" {
			req.Header.Set("If-Match", info.StrongETag)
		}

		resp, err := d.cfg.Client.Do(req)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("download request: %w", err)
			if attempt+1 < d.cfg.MaxAttempts {
				if err := d.sleepBeforeRetry(ctx, attempt, ""); err != nil {
					return err
				}
				continue
			}
			break
		}

		aerr := d.consumeSingleResponse(ctx, resp, f, info, tracker)
		cancel()
		if aerr == nil {
			if err := f.Sync(); err != nil {
				return fmt.Errorf("sync partial file: %w", err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close partial file: %w", err)
			}
			closed = true
			if err := verifySHA256(partPath, d.cfg.ExpectedSHA256); err != nil {
				return err
			}
			if err := os.Chmod(partPath, d.cfg.FileMode); err != nil {
				return fmt.Errorf("chmod completed file: %w", err)
			}
			if err := d.finalize(partPath, dest); err != nil {
				return err
			}
			tracker.force()
			return nil
		}

		lastErr = aerr.err
		if errors.Is(aerr.err, ErrSourceChanged) || !aerr.retryable || attempt+1 >= d.cfg.MaxAttempts {
			break
		}
		if err := d.sleepBeforeRetry(ctx, attempt, aerr.retryAfter); err != nil {
			return err
		}
	}
	return lastErr
}

func (d *Downloader) consumeSingleResponse(
	ctx context.Context,
	resp *http.Response,
	f *os.File,
	info probeInfo,
	tracker *progressTracker,
) *attemptError {
	defer resp.Body.Close()
	if enc := strings.TrimSpace(resp.Header.Get("Content-Encoding")); enc != "" && !strings.EqualFold(enc, "identity") {
		return &attemptError{err: fmt.Errorf("server returned unsupported Content-Encoding %q", enc)}
	}
	if resp.StatusCode == http.StatusPreconditionFailed {
		return &attemptError{err: ErrSourceChanged}
	}
	if resp.StatusCode != http.StatusOK {
		body := readSmallBody(resp.Body)
		return &attemptError{
			err:        fmt.Errorf("download: HTTP %s%s", resp.Status, bodySuffix(body)),
			retryable:  retryableStatus(resp.StatusCode),
			retryAfter: resp.Header.Get("Retry-After"),
		}
	}

	if info.StrongETag != "" {
		if got := strongETag(resp.Header.Get("ETag")); got != info.StrongETag {
			return &attemptError{err: ErrSourceChanged}
		}
		if info.Size >= 0 && resp.ContentLength >= 0 && resp.ContentLength != info.Size {
			return &attemptError{err: ErrSourceChanged}
		}
	}

	total := resp.ContentLength
	if info.StrongETag != "" && info.Size >= 0 {
		total = info.Size
	}
	tracker.setTotal(total)

	buf := make([]byte, 256<<10)
	written, rerr, werr := copyStream(ctx, resp.Body, f, buf, tracker)
	if werr != nil {
		return &attemptError{err: fmt.Errorf("write partial file: %w", werr)}
	}
	if rerr != nil {
		return &attemptError{err: fmt.Errorf("read response body: %w", rerr), retryable: true}
	}
	if total >= 0 && written != total {
		return &attemptError{err: fmt.Errorf("short response: got %d bytes, want %d", written, total), retryable: true}
	}
	return nil
}

func copyStream(
	ctx context.Context,
	r io.Reader,
	w io.Writer,
	buf []byte,
	tracker *progressTracker,
) (written int64, readErr, writeErr error) {
	for {
		if err := ctx.Err(); err != nil {
			return written, err, nil
		}
		nr, er := r.Read(buf)
		if nr > 0 {
			nw, ew := w.Write(buf[:nr])
			written += int64(nw)
			tracker.add(int64(nw), false)
			if ew != nil {
				return written, nil, ew
			}
			if nw != nr {
				return written, nil, io.ErrShortWrite
			}
		}
		if er != nil {
			if errors.Is(er, io.EOF) {
				return written, nil, nil
			}
			return written, er, nil
		}
	}
}

func (d *Downloader) downloadParallel(
	ctx context.Context,
	rawURL, dest, partPath, metaPath string,
	info probeInfo,
	tracker *progressTracker,
) error {
	identity := d.cfg.ResumeKey
	if identity == "" {
		identity = rawURL
	}

	st, resumed, err := d.loadOrCreateState(partPath, metaPath, identity, info)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(partPath, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open partial file: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	state := &stateTracker{st: st}
	completed := state.completedBytes()
	tracker.setCompleted(completed, true)
	_ = resumed // kept for future logging/metrics hooks

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var firstErr error
	var errOnce sync.Once
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	checkpointStop := make(chan struct{})
	checkpointDone := make(chan struct{})
	if d.cfg.CheckpointInterval > 0 {
		go func() {
			defer close(checkpointDone)
			ticker := time.NewTicker(d.cfg.CheckpointInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					snap := state.snapshot()
					if err := f.Sync(); err != nil {
						fail(fmt.Errorf("checkpoint file sync: %w", err))
						return
					}
					if err := writeJSONAtomic(metaPath, snap); err != nil {
						fail(fmt.Errorf("checkpoint metadata: %w", err))
						return
					}
				case <-checkpointStop:
					return
				case <-workCtx.Done():
					return
				}
			}
		}()
	} else {
		close(checkpointDone)
	}

	jobs := make(chan int)
	workers := d.cfg.Workers
	if workers > st.Chunks {
		workers = st.Chunks
	}
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			buf := make([]byte, 256<<10)
			for chunk := range jobs {
				if state.isDone(chunk) {
					continue
				}
				start, end := chunkBounds(chunk, info.Size, d.cfg.ChunkSize)
				if err := d.downloadChunk(workCtx, rawURL, f, start, end, info, buf); err != nil {
					if workCtx.Err() != nil && !errors.Is(err, ErrSourceChanged) && !errors.Is(err, ErrRangeUnsupported) {
						return
					}
					fail(err)
					return
				}
				if state.markDone(chunk) {
					tracker.add(end-start+1, false)
				}
			}
		}()
	}

feedLoop:
	for chunk := 0; chunk < st.Chunks; chunk++ {
		if state.isDone(chunk) {
			continue
		}
		select {
		case jobs <- chunk:
		case <-workCtx.Done():
			break feedLoop
		}
	}
	close(jobs)
	wg.Wait()
	close(checkpointStop)
	<-checkpointDone

	if firstErr != nil {
		return firstErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !state.allDone() {
		return errors.New("download stopped before all chunks completed")
	}

	// Persist a final complete checkpoint. The ordering is intentional:
	// data sync first, then metadata claiming completion.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("final file sync: %w", err)
	}
	if err := writeJSONAtomic(metaPath, state.snapshot()); err != nil {
		return fmt.Errorf("final metadata checkpoint: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close partial file: %w", err)
	}
	closed = true

	if err := verifySHA256(partPath, d.cfg.ExpectedSHA256); err != nil {
		return err
	}
	if err := os.Chmod(partPath, d.cfg.FileMode); err != nil {
		return fmt.Errorf("chmod completed file: %w", err)
	}
	if err := d.finalize(partPath, dest); err != nil {
		return err
	}
	// The destination is already committed; stale metadata cleanup is best effort.
	_ = os.Remove(metaPath)
	_ = syncParent(metaPath)
	tracker.force()
	return nil
}

func (d *Downloader) loadOrCreateState(
	partPath, metaPath, identity string,
	info probeInfo,
) (resumeState, bool, error) {
	chunks64 := (info.Size + d.cfg.ChunkSize - 1) / d.cfg.ChunkSize
	if chunks64 <= 0 || chunks64 > int64(^uint(0)>>1) {
		return resumeState{}, false, fmt.Errorf("invalid number of chunks: %d", chunks64)
	}
	chunks := int(chunks64)
	want := resumeState{
		Version:   1,
		Identity:  identity,
		Size:      info.Size,
		ETag:      info.StrongETag,
		ChunkSize: d.cfg.ChunkSize,
		Chunks:    chunks,
		Completed: make([]byte, (chunks+7)/8),
	}

	data, err := os.ReadFile(metaPath)
	if err == nil {
		var got resumeState
		if json.Unmarshal(data, &got) == nil && stateCompatible(got, want) {
			fi, statErr := os.Lstat(partPath)
			if statErr == nil && fi.Mode().IsRegular() && fi.Size() == info.Size {
				return got, true, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return resumeState{}, false, fmt.Errorf("read resume metadata: %w", err)
	}

	_ = os.Remove(metaPath)
	_ = os.Remove(partPath)
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return resumeState{}, false, fmt.Errorf("create partial file: %w", err)
	}
	if err := f.Truncate(info.Size); err != nil {
		_ = f.Close()
		_ = os.Remove(partPath)
		return resumeState{}, false, fmt.Errorf("size partial file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(partPath)
		return resumeState{}, false, fmt.Errorf("sync new partial file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(partPath)
		return resumeState{}, false, fmt.Errorf("close new partial file: %w", err)
	}
	if err := writeJSONAtomic(metaPath, want); err != nil {
		_ = os.Remove(partPath)
		return resumeState{}, false, fmt.Errorf("write initial resume metadata: %w", err)
	}
	return want, false, nil
}

func stateCompatible(got, want resumeState) bool {
	return got.Version == want.Version &&
		got.Identity == want.Identity &&
		got.Size == want.Size &&
		got.ETag == want.ETag &&
		got.ChunkSize == want.ChunkSize &&
		got.Chunks == want.Chunks &&
		len(got.Completed) == len(want.Completed)
}

func (d *Downloader) downloadChunk(
	ctx context.Context,
	rawURL string,
	f *os.File,
	start, end int64,
	info probeInfo,
	buf []byte,
) error {
	var lastErr error
	for attempt := 0; attempt < d.cfg.MaxAttempts; attempt++ {
		aerr := d.downloadChunkOnce(ctx, rawURL, f, start, end, info, buf)
		if aerr == nil {
			return nil
		}
		lastErr = aerr.err
		if errors.Is(aerr.err, ErrSourceChanged) || errors.Is(aerr.err, ErrRangeUnsupported) || !aerr.retryable || attempt+1 >= d.cfg.MaxAttempts {
			break
		}
		if err := d.sleepBeforeRetry(ctx, attempt, aerr.retryAfter); err != nil {
			return err
		}
	}
	return lastErr
}

func (d *Downloader) downloadChunkOnce(
	ctx context.Context,
	rawURL string,
	f *os.File,
	start, end int64,
	info probeInfo,
	buf []byte,
) *attemptError {
	reqCtx, cancel := d.requestContext(ctx)
	defer cancel()

	req, err := d.newRequest(reqCtx, http.MethodGet, rawURL)
	if err != nil {
		return &attemptError{err: err}
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	req.Header.Set("If-Range", info.StrongETag)

	resp, err := d.cfg.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return &attemptError{err: ctx.Err()}
		}
		return &attemptError{err: fmt.Errorf("range %d-%d request: %w", start, end, err), retryable: true}
	}
	defer resp.Body.Close()

	if enc := strings.TrimSpace(resp.Header.Get("Content-Encoding")); enc != "" && !strings.EqualFold(enc, "identity") {
		return &attemptError{err: fmt.Errorf("range %d-%d returned unsupported Content-Encoding %q", start, end, enc)}
	}

	if resp.StatusCode == http.StatusOK {
		got := strongETag(resp.Header.Get("ETag"))
		if got == info.StrongETag {
			return &attemptError{err: ErrRangeUnsupported}
		}
		return &attemptError{err: ErrSourceChanged}
	}
	if resp.StatusCode == http.StatusPreconditionFailed || resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return &attemptError{err: ErrSourceChanged}
	}
	if resp.StatusCode != http.StatusPartialContent {
		body := readSmallBody(resp.Body)
		return &attemptError{
			err:        fmt.Errorf("range %d-%d: HTTP %s%s", start, end, resp.Status, bodySuffix(body)),
			retryable:  retryableStatus(resp.StatusCode),
			retryAfter: resp.Header.Get("Retry-After"),
		}
	}

	if got := strongETag(resp.Header.Get("ETag")); got != info.StrongETag {
		return &attemptError{err: ErrSourceChanged}
	}
	gotStart, gotEnd, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || gotStart != start || gotEnd != end || total != info.Size {
		return &attemptError{err: fmt.Errorf("%w: unexpected Content-Range %q", ErrSourceChanged, resp.Header.Get("Content-Range"))}
	}

	want := end - start + 1
	if resp.ContentLength >= 0 && resp.ContentLength != want {
		return &attemptError{err: fmt.Errorf("range %d-%d has Content-Length %d, want %d", start, end, resp.ContentLength, want)}
	}
	readErr, writeErr := copyExactAt(reqCtx, resp.Body, f, start, want, buf)
	if writeErr != nil {
		return &attemptError{err: fmt.Errorf("write range %d-%d: %w", start, end, writeErr)}
	}
	if readErr != nil {
		if ctx.Err() != nil {
			return &attemptError{err: ctx.Err()}
		}
		return &attemptError{err: fmt.Errorf("read range %d-%d: %w", start, end, readErr), retryable: true}
	}
	if err := requireEOF(resp.Body); err != nil {
		return &attemptError{err: fmt.Errorf("range %d-%d framing: %w", start, end, err), retryable: true}
	}
	return nil
}

func copyExactAt(
	ctx context.Context,
	r io.Reader,
	f *os.File,
	offset, length int64,
	buf []byte,
) (readErr, writeErr error) {
	remaining := length
	pos := offset
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err, nil
		}
		want := int64(len(buf))
		if want > remaining {
			want = remaining
		}
		nr, er := r.Read(buf[:int(want)])
		if nr > 0 {
			nw, ew := f.WriteAt(buf[:nr], pos)
			pos += int64(nw)
			remaining -= int64(nw)
			if ew != nil {
				return nil, ew
			}
			if nw != nr {
				return nil, io.ErrShortWrite
			}
		}
		if remaining == 0 {
			return nil, nil
		}
		if er != nil {
			if errors.Is(er, io.EOF) {
				return io.ErrUnexpectedEOF, nil
			}
			return er, nil
		}
		if nr == 0 {
			return io.ErrNoProgress, nil
		}
	}
	return nil, nil
}

func (d *Downloader) sleepBeforeRetry(ctx context.Context, attempt int, retryAfter string) error {
	delay := parseRetryAfter(retryAfter, time.Now())
	if delay < 0 {
		max := d.cfg.BaseBackoff
		for i := 0; i < attempt && max < d.cfg.MaxBackoff; i++ {
			if max > d.cfg.MaxBackoff/2 {
				max = d.cfg.MaxBackoff
				break
			}
			max *= 2
		}
		if max > d.cfg.MaxBackoff {
			max = d.cfg.MaxBackoff
		}
		delay = fullJitter(max)
	}
	if delay > d.cfg.MaxBackoff {
		delay = d.cfg.MaxBackoff
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

func fullJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return max / 2
	}
	n := binary.LittleEndian.Uint64(b[:])
	return time.Duration(n % uint64(max+1))
}

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

func parseContentRange(value string) (start, end, total int64, ok bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bytes") {
		return 0, 0, 0, false
	}
	parts := strings.SplitN(fields[1], "/", 2)
	if len(parts) != 2 || parts[0] == "*" || parts[1] == "*" {
		return 0, 0, 0, false
	}
	span := strings.SplitN(parts[0], "-", 2)
	if len(span) != 2 {
		return 0, 0, 0, false
	}
	start, err1 := strconv.ParseInt(span[0], 10, 64)
	end, err2 := strconv.ParseInt(span[1], 10, 64)
	total, err3 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

func parseUnsatisfiedContentRange(value string) (total int64, ok bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bytes") {
		return 0, false
	}
	if !strings.HasPrefix(fields[1], "*/") {
		return 0, false
	}
	total, err := strconv.ParseInt(strings.TrimPrefix(fields[1], "*/"), 10, 64)
	return total, err == nil && total >= 0
}

func strongETag(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "W/") {
		return ""
	}
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return ""
	}
	return value
}

func chunkBounds(chunk int, size, chunkSize int64) (start, end int64) {
	start = int64(chunk) * chunkSize
	end = start + chunkSize - 1
	if end >= size {
		end = size - 1
	}
	return start, end
}

func (s *stateTracker) isDone(chunk int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bitIsSet(s.st.Completed, chunk)
}

func (s *stateTracker) markDone(chunk int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bitIsSet(s.st.Completed, chunk) {
		return false
	}
	setBit(s.st.Completed, chunk)
	return true
}

func (s *stateTracker) snapshot() resumeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.st
	out.Completed = append([]byte(nil), s.st.Completed...)
	return out
}

func (s *stateTracker) allDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < s.st.Chunks; i++ {
		if !bitIsSet(s.st.Completed, i) {
			return false
		}
	}
	return true
}

func (s *stateTracker) completedBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for i := 0; i < s.st.Chunks; i++ {
		if bitIsSet(s.st.Completed, i) {
			start, end := chunkBounds(i, s.st.Size, s.st.ChunkSize)
			total += end - start + 1
		}
	}
	return total
}

func bitIsSet(bits []byte, i int) bool {
	return bits[i/8]&(1<<uint(i%8)) != 0
}

func setBit(bits []byte, i int) {
	bits[i/8] |= 1 << uint(i%8)
}

func newProgressTracker(total int64, interval time.Duration, callback func(Progress)) *progressTracker {
	t := &progressTracker{interval: interval, callback: callback}
	t.total.Store(total)
	return t
}

func (t *progressTracker) setTotal(total int64) {
	t.total.Store(total)
}

func (t *progressTracker) setCompleted(n int64, force bool) {
	t.done.Store(n)
	t.emit(force)
}

func (t *progressTracker) add(n int64, force bool) {
	t.done.Add(n)
	t.emit(force)
}

func (t *progressTracker) force() {
	t.emit(true)
}

func (t *progressTracker) emit(force bool) {
	if t.callback == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if !force && t.interval > 0 && !t.last.IsZero() && now.Sub(t.last) < t.interval {
		return
	}
	t.last = now
	done := t.done.Load()
	total := t.total.Load()
	percent := 0.0
	if total > 0 {
		percent = float64(done) * 100 / float64(total)
		if percent > 100 {
			percent = 100
		}
	}
	t.callback(Progress{Completed: done, Total: total, Percent: percent})
}

func writeJSONAtomic(path string, value any) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanup = false
	return syncParent(path)
}

func (d *Downloader) finalize(partPath, dest string) error {
	if !d.cfg.Overwrite {
		if _, err := os.Lstat(dest); err == nil {
			return fmt.Errorf("%w: %s", ErrDestinationExists, dest)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat destination before rename: %w", err)
		}
	}
	if err := os.Rename(partPath, dest); err != nil {
		return fmt.Errorf("rename completed file: %w", err)
	}
	_ = syncParent(dest)
	return nil
}

func syncParent(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		// Some platforms/filesystems do not support syncing directories.
		// The file itself has already been synced, so treat this as best effort.
		return nil
	}
	return nil
}

func acquireLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: %s (remove it only if no downloader is running)", ErrLocked, path)
		}
		return nil, fmt.Errorf("create lock file: %w", err)
	}
	_, _ = fmt.Fprintf(f, "pid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
	_ = f.Sync()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close lock file: %w", err)
	}
	return func() { _ = os.Remove(path) }, nil
}

func verifySHA256(path, expected string) error {
	if expected == "" {
		return nil
	}
	want, err := parseSHA256(expected)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file for SHA-256: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("calculate SHA-256: %w", err)
	}
	got := h.Sum(nil)
	if !equalBytes(got, want) {
		return fmt.Errorf("SHA-256 mismatch: got %s, want %s", hex.EncodeToString(got), expected)
	}
	return nil
}

func parseSHA256(value string) ([]byte, error) {
	if len(value) != sha256.Size*2 {
		return nil, fmt.Errorf("expected SHA-256 must contain exactly 64 hex characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("invalid SHA-256: %w", err)
	}
	return decoded, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func requireEOF(r io.Reader) error {
	var one [1]byte
	for i := 0; i < 3; i++ {
		n, err := r.Read(one[:])
		if n > 0 {
			return errors.New("response body contains more bytes than declared")
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return io.ErrNoProgress
}

func readSmallBody(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 4<<10))
	return strings.TrimSpace(string(data))
}

func bodySuffix(body string) string {
	if body == "" {
		return ""
	}
	return ": " + body
}
