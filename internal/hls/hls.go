// Package hls downloads HTTP Live Streaming (RFC 8216) video-on-demand streams
// into a single file, as specified in DESIGN.md sections 3.7 and 3.10.
package hls

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zichuanxu/nimget/internal/filelock"
)

var (
	ErrLive              = errors.New("live HLS playlists are not supported (no EXT-X-ENDLIST)")
	ErrUnsupported       = errors.New("unsupported HLS feature")
	ErrDestinationExists = errors.New("destination already exists")
	ErrLocked            = errors.New("destination is locked by another download")
)

// Limiter throttles body reads; *rate.Limiter from golang.org/x/time/rate
// satisfies it.
type Limiter interface {
	WaitN(ctx context.Context, n int) error
	Burst() int
}

// Options configures Download. Zero values select the defaults.
type Options struct {
	Client                  *http.Client // default: DefaultTransport clone without compression
	Headers                 http.Header  // sent with every request
	UserAgent               string       // default "nimget/1.0"
	Concurrency             int          // parallel segment fetches, default 4
	MaxAttempts             int          // per request, default 5
	BaseBackoff, MaxBackoff time.Duration
	StallTimeout            time.Duration // fail a request idle this long, default 30s
	Limiters                []Limiter
}

// Progress reports a download's state. Callbacks are serialized.
type Progress struct {
	Completed              int64 // bytes so far, including resumed segments
	Total                  int64 // estimate; -1 until a few segments are done
	Segments, SegmentsDone int   // media segments plus init sections
}

func (o *Options) normalize() {
	if o.Client == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DisableCompression = true
		o.Client = &http.Client{Transport: t}
	}
	if o.UserAgent == "" {
		o.UserAgent = "nimget/1.0"
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	for _, d := range []struct {
		v   *time.Duration
		def time.Duration
	}{{&o.BaseBackoff, 300 * time.Millisecond}, {&o.MaxBackoff, 10 * time.Second}, {&o.StallTimeout, 30 * time.Second}} {
		if *d.v <= 0 {
			*d.v = d.def
		}
	}
	o.MaxBackoff = max(o.MaxBackoff, o.BaseBackoff)
}

type downloader struct {
	opts Options
	// origin is the playlist's host, the only one that gets every header.
	origin string
	prog   *tracker
	mu     sync.Mutex
	keys   map[string]*keyEntry
}

type keyEntry struct {
	once sync.Once
	key  []byte
	err  error
}

// Download fetches the playlist at rawURL and writes the stream to dest. It
// resumes from the segments kept by an earlier interrupted run, provided the
// playlist is unchanged.
func Download(ctx context.Context, rawURL, dest string, opts Options, progress func(Progress)) error {
	opts.normalize()
	release, err := lock(dest)
	if err != nil {
		return err
	}
	defer release()
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("%w: %s", ErrDestinationExists, dest)
	}

	d := &downloader{opts: opts, prog: &tracker{cb: progress}, keys: map[string]*keyEntry{}}
	if u, err := url.Parse(rawURL); err == nil {
		d.origin = u.Hostname()
	}
	items, err := d.resolve(ctx, rawURL)
	if err != nil {
		return err
	}
	work := dest + ".hls"
	if err := prepareWorkDir(work, items); err != nil {
		return err
	}
	d.prog.segments = len(items)
	var pending []int
	for i := range items {
		if fi, err := os.Stat(segPath(work, i)); err == nil {
			d.prog.live.Add(fi.Size())
			d.prog.done++
			d.prog.doneBytes += fi.Size()
		} else {
			pending = append(pending, i)
		}
	}
	d.prog.report(true)
	if err := d.fetchAll(ctx, work, items, pending); err != nil {
		d.prog.report(true)
		return err
	}
	return commit(work, dest, len(items))
}

// Discard removes the partial state kept for dest.
func Discard(dest string) error {
	release, err := lock(dest)
	if err != nil {
		return err
	}
	defer release()
	if err := removeWorkDir(dest + ".hls"); err != nil {
		return err
	}
	if err := os.Remove(dest + ".part"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Detect reports whether a response looks like an HLS playlist, judging by its
// Content-Type, its URL path or the first bytes of its body.
func Detect(contentType, rawURL string, head []byte) bool {
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		switch mt {
		// Not audio/mpegurl: plain M3U radio playlists use it too.
		case "application/vnd.apple.mpegurl", "application/x-mpegurl":
			return true
		}
	}
	if u, err := url.Parse(rawURL); err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".m3u8") {
		return true
	}
	return bytes.HasPrefix(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), []byte("#EXTM3U"))
}

func lock(dest string) (func() error, error) {
	release, err := filelock.Acquire(dest + ".lock")
	if errors.Is(err, filelock.ErrLocked) {
		return nil, fmt.Errorf("%w: %s", ErrLocked, dest)
	}
	return release, err
}

// resolve fetches the playlist, following a master playlist to its best variant.
func (d *downloader) resolve(ctx context.Context, rawURL string) ([]item, error) {
	for range 2 {
		body, final, err := d.get(ctx, rawURL)
		if err != nil {
			return nil, fmt.Errorf("playlist: %w", err)
		}
		variant, items, err := parsePlaylist(body, final)
		if err != nil || variant == "" {
			return items, err
		}
		rawURL = variant
	}
	return nil, fmt.Errorf("variant playlist %s is itself a master playlist", rawURL)
}

// fetchAll downloads the pending items with bounded concurrency. The first
// failure cancels the rest.
func (d *downloader) fetchAll(ctx context.Context, work string, items []item, pending []int) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(d.opts.Concurrency, len(pending)) {
		wg.Go(func() {
			for i := range next {
				if err := d.fetchItem(ctx, work, i, items[i]); err != nil {
					cancel(err)
					return
				}
			}
		})
	}
feed:
	for _, i := range pending {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	return context.Cause(ctx)
}

// fetchItem streams one item to a temporary file in the work dir, decrypts
// it into its segment file, and renames that into place.
func (d *downloader) fetchItem(ctx context.Context, work string, i int, it item) error {
	var key []byte
	if it.KeyURI != "" {
		var err error
		if key, err = d.key(ctx, it.KeyURI); err != nil {
			return err
		}
	}
	raw, err := os.CreateTemp(work, ".dl-*")
	if err != nil {
		return err
	}
	defer os.Remove(raw.Name())
	defer raw.Close()
	_, size, err := d.fetch(ctx, it.URI, it.Off, it.Len, true, -1, fileSink{raw})
	if err != nil {
		return fmt.Errorf("segment %d: %w", i, err)
	}
	out := raw
	if key != nil {
		if out, err = os.CreateTemp(work, ".dec-*"); err != nil {
			return err
		}
		defer os.Remove(out.Name())
		defer out.Close()
		if err := decryptFile(raw, out, size, key, it.IV); err != nil {
			return fmt.Errorf("segment %d: %w", i, err)
		}
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(out.Name(), segPath(work, i)); err != nil {
		return fmt.Errorf("segment %d: %w", i, err)
	}
	d.prog.finish(size)
	return nil
}

// key fetches each key URI once; a failure is final for the whole download.
func (d *downloader) key(ctx context.Context, uri string) ([]byte, error) {
	d.mu.Lock()
	e := d.keys[uri]
	if e == nil {
		e = &keyEntry{}
		d.keys[uri] = e
	}
	d.mu.Unlock()
	e.once.Do(func() {
		body, _, err := d.get(ctx, uri)
		switch {
		case err != nil:
			e.err = fmt.Errorf("key %s: %w", uri, err)
		case len(body) != 16:
			e.err = fmt.Errorf("key %s: got %d bytes, want 16", uri, len(body))
		default:
			e.key = body
		}
	})
	return e.key, e.err
}

// decryptFile reverses AES-128-CBC with PKCS#7 padding, streaming size
// bytes of in to out; only the final block's padding is held back.
func decryptFile(in *os.File, out io.Writer, size int64, key, iv []byte) error {
	if size == 0 || size%aes.BlockSize != 0 {
		return fmt.Errorf("decrypt: ciphertext length %d is not a positive multiple of 16", size)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return err
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	buf := make([]byte, 64<<10) // a multiple of the block size
	for left := size; left > 0; {
		chunk := buf[:min(int64(len(buf)), left)]
		if _, err := io.ReadFull(in, chunk); err != nil {
			return fmt.Errorf("decrypt: %w", err)
		}
		mode.CryptBlocks(chunk, chunk)
		if left -= int64(len(chunk)); left == 0 {
			p := int(chunk[len(chunk)-1])
			if p == 0 || p > aes.BlockSize || !bytes.Equal(chunk[len(chunk)-p:], bytes.Repeat([]byte{byte(p)}, p)) {
				return errors.New("decrypt: bad PKCS#7 padding (wrong key or IV?)")
			}
			chunk = chunk[:len(chunk)-p]
		}
		if _, err := out.Write(chunk); err != nil {
			return err
		}
	}
	return nil
}

func segPath(work string, i int) string { return filepath.Join(work, fmt.Sprintf("%06d.seg", i)) }

// prepareWorkDir keeps the work dir only if it belongs to the same playlist.
// The fingerprint leaves out query strings, which often carry expiring
// tokens: a re-fetched playlist with fresh tokens still resumes, and its
// fresh URLs are used for the remaining segments.
func prepareWorkDir(work string, items []item) error {
	stable := make([]item, len(items))
	for i, it := range items {
		it.URI, it.KeyURI = withoutQuery(it.URI), withoutQuery(it.KeyURI)
		stable[i] = it
	}
	raw, err := json.Marshal(stable)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	state, _ := json.Marshal(map[string]string{"fingerprint": hex.EncodeToString(sum[:])})
	statePath := filepath.Join(work, "state.json")
	got, err := os.ReadFile(statePath)
	if err == nil && bytes.Equal(got, state) {
		return nil
	}
	if err := removeWorkDir(work); err != nil {
		return err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	return writeFileSynced(statePath, state)
}

// removeWorkDir deletes work only if it is nimget's: it holds state.json.
func removeWorkDir(work string) error {
	if _, err := os.Lstat(work); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(work, "state.json")); err != nil {
		return fmt.Errorf("%s exists and is not a nimget download directory", work)
	}
	if err := os.RemoveAll(work); err != nil {
		return fmt.Errorf("discard stale work dir: %w", err)
	}
	return nil
}

func withoutQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// writeFileSynced writes path atomically: temp file, fsync, rename.
func writeFileSynced(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// commit concatenates the items into dest.part and places it at dest.
//
// ponytail: the work dir doubles disk use until this concat; to avoid it,
// append items to .part in order as they finish and persist an in-order
// watermark instead of keeping per-segment files.
func commit(work, dest string, n int) error {
	part := dest + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create partial file: %w", err)
	}
	defer f.Close()
	for i := range n {
		seg, err := os.Open(segPath(work, i))
		if err != nil {
			return err
		}
		_, err = io.Copy(f, seg)
		seg.Close()
		if err != nil {
			return fmt.Errorf("concatenate segment %d: %w", i, err)
		}
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync partial file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close partial file: %w", err)
	}
	if err := place(part, dest); err != nil {
		return err
	}
	syncDir(filepath.Dir(dest))
	_ = os.RemoveAll(work) // dest is committed; leftovers are harmless
	return nil
}

// place moves part to dest without overwriting: a hard link fails atomically
// if dest exists; file systems without links fall back to check-then-rename.
func place(part, dest string) error {
	err := os.Link(part, dest)
	if err == nil {
		_ = os.Remove(part)
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s", ErrDestinationExists, dest)
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("%w: %s", ErrDestinationExists, dest)
	}
	if err := os.Rename(part, dest); err != nil {
		return fmt.Errorf("rename completed file: %w", err)
	}
	return nil
}

// syncDir makes a rename durable; platforms that cannot sync directories are
// ignored because the file itself is already synced.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// tracker serializes progress callbacks: every finished item reports, byte
// updates at most every 250 ms.
type tracker struct {
	cb   func(Progress)
	live atomic.Int64

	mu             sync.Mutex
	last           time.Time
	segments, done int
	doneBytes      int64
}

func (t *tracker) add(n int64) {
	t.live.Add(n)
	t.report(false)
}

func (t *tracker) finish(n int64) {
	t.mu.Lock()
	t.done++
	t.doneBytes += n
	t.mu.Unlock()
	t.report(true)
}

func (t *tracker) report(force bool) {
	if t.cb == nil {
		return
	}
	if force {
		t.mu.Lock()
	} else if !t.mu.TryLock() {
		return
	}
	defer t.mu.Unlock()
	now := time.Now()
	if !force && now.Sub(t.last) < 250*time.Millisecond {
		return
	}
	t.last = now
	p := Progress{Completed: t.live.Load(), Total: -1, Segments: t.segments, SegmentsDone: t.done}
	if t.done > 0 && t.done >= min(3, t.segments) {
		p.Total = max(p.Completed, t.doneBytes+t.doneBytes/int64(t.done)*int64(t.segments-t.done))
	}
	t.cb(p)
}
