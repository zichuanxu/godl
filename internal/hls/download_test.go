package hls

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/filelock"
)

// server serves static files, counting requests per path. A hook may take
// over a request by returning true.
type server struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	hits  map[string]int
	hook  func(w http.ResponseWriter, r *http.Request, hit int) bool
}

func newServer(t *testing.T) *server {
	s := &server{files: map[string][]byte{}, hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		hit, data, ok, hook := s.hits[r.URL.Path], s.files[r.URL.Path], s.files[r.URL.Path] != nil, s.hook
		s.mu.Unlock()
		if hook != nil && hook(w, r, hit) {
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) set(path string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = data
}

func (s *server) setHook(h func(w http.ResponseWriter, r *http.Request, hit int) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = h
}

func (s *server) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// payload returns deterministic bytes of an odd size so padding is exercised.
func payload(seed, size int) []byte {
	r := rand.New(rand.NewPCG(uint64(seed), 7))
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func encrypt(key, iv, plain []byte) []byte {
	p := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append(slices.Clone(plain), bytes.Repeat([]byte{byte(p)}, p)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(buf, buf)
	return buf
}

// clearStream serves n TS segments and a media playlist, returning the
// expected output.
func clearStream(s *server, n int) []byte {
	var pl strings.Builder
	var want []byte
	pl.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
	for i := range n {
		seg := payload(i, 3000+i*97)
		s.set(fmt.Sprintf("/s%d.ts", i), seg)
		want = append(want, seg...)
		fmt.Fprintf(&pl, "#EXTINF:4.0,\ns%d.ts\n", i)
	}
	pl.WriteString("#EXT-X-ENDLIST\n")
	s.set("/media.m3u8", []byte(pl.String()))
	return want
}

func fastOpts() Options {
	return Options{Concurrency: 3, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}
}

func destPath(t *testing.T) string { return filepath.Join(t.TempDir(), "out.ts") }

func assertFile(t *testing.T, dest string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output differs: got %d bytes, want %d", len(got), len(want))
	}
	for _, leftover := range []string{".hls", ".part", ".lock"} {
		if _, err := os.Stat(dest + leftover); !os.IsNotExist(err) {
			t.Errorf("%s left behind: %v", leftover, err)
		}
	}
}

func TestClearTSViaMaster(t *testing.T) {
	s := newServer(t)
	want := clearStream(s, 6)
	s.set("/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nnope.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=9\nmedia.m3u8\n"))
	dest := destPath(t)
	var last Progress
	var calls atomic.Int32
	err := Download(context.Background(), s.URL+"/master.m3u8", dest, fastOpts(), func(p Progress) {
		calls.Add(1)
		last = p
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, want)
	if last.SegmentsDone != 6 || last.Segments != 6 || last.Completed != int64(len(want)) || last.Total != int64(len(want)) {
		t.Fatalf("final progress = %+v, want %d bytes", last, len(want))
	}
	if s.count("/nope.m3u8") != 0 {
		t.Fatal("fetched the low-bandwidth variant")
	}
}

func TestAES128(t *testing.T) {
	s := newServer(t)
	k1, k2 := payload(100, 16), payload(101, 16)
	explicitIV := payload(102, 16)
	s.set("/k1.key", k1)
	s.set("/k2.key", k2)
	var want []byte
	pl := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:5\n"
	pl += fmt.Sprintf("#EXT-X-KEY:METHOD=AES-128,URI=\"k1.key\",IV=0x%x\n", explicitIV)
	for i := range 6 {
		plain := payload(i, 2000+i*33)
		want = append(want, plain...)
		var body []byte
		switch {
		case i < 2: // explicit IV
			body = encrypt(k1, explicitIV, plain)
		case i < 4: // rotated key, IV from media sequence number 5+i
			if i == 2 {
				pl += "#EXT-X-KEY:METHOD=AES-128,URI=\"k2.key\"\n"
			}
			body = encrypt(k2, seqIV(int64(5+i)), plain)
		default:
			if i == 4 {
				pl += "#EXT-X-KEY:METHOD=NONE\n"
			}
			body = plain
		}
		s.set(fmt.Sprintf("/s%d.ts", i), body)
		pl += fmt.Sprintf("#EXTINF:4,\ns%d.ts\n", i)
	}
	s.set("/media.m3u8", []byte(pl+"#EXT-X-ENDLIST\n"))

	dest := destPath(t)
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, fastOpts(), nil); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, want)
	if s.count("/k1.key") != 1 || s.count("/k2.key") != 1 {
		t.Fatalf("keys fetched %d/%d times, want once each", s.count("/k1.key"), s.count("/k2.key"))
	}
}

func TestFMP4Map(t *testing.T) {
	init1, init2 := payload(50, 700), payload(51, 600)
	segs := [][]byte{payload(0, 3000), payload(1, 2500), payload(2, 2800)}
	want := slices.Concat(init1, segs[0], segs[1], init2, segs[2])

	t.Run("separate files", func(t *testing.T) {
		s := newServer(t)
		s.set("/init1.mp4", init1)
		s.set("/init2.mp4", init2)
		for i, seg := range segs {
			s.set(fmt.Sprintf("/s%d.m4s", i), seg)
		}
		s.set("/media.m3u8", []byte(`#EXTM3U
#EXT-X-MAP:URI="init1.mp4"
#EXTINF:4,
s0.m4s
#EXT-X-MAP:URI="init1.mp4"
#EXTINF:4,
s1.m4s
#EXT-X-MAP:URI="init2.mp4"
#EXTINF:4,
s2.m4s
#EXT-X-ENDLIST
`))
		dest := destPath(t)
		if err := Download(context.Background(), s.URL+"/media.m3u8", dest, fastOpts(), nil); err != nil {
			t.Fatal(err)
		}
		assertFile(t, dest, want)
	})

	// One file holds everything: init1, s0, s1, init2, s2.
	byteranges := func(t *testing.T, ignoreRange bool) {
		s := newServer(t)
		s.set("/all.mp4", want)
		if ignoreRange {
			s.setHook(func(w http.ResponseWriter, r *http.Request, _ int) bool {
				r.Header.Del("Range")
				return false
			})
		}
		o1, o2 := len(init1)+len(segs[0])+len(segs[1]), len(want)-len(segs[2])
		s.set("/media.m3u8", fmt.Appendf(nil, `#EXTM3U
#EXT-X-MAP:URI="all.mp4",BYTERANGE="%d@0"
#EXTINF:4,
#EXT-X-BYTERANGE:%d@%d
all.mp4
#EXTINF:4,
#EXT-X-BYTERANGE:%d
all.mp4
#EXT-X-MAP:URI="all.mp4",BYTERANGE="%d@%d"
#EXTINF:4,
#EXT-X-BYTERANGE:%d@%d
all.mp4
#EXT-X-ENDLIST
`, len(init1), len(segs[0]), len(init1), len(segs[1]), len(init2), o1, len(segs[2]), o2))
		dest := destPath(t)
		if err := Download(context.Background(), s.URL+"/media.m3u8", dest, fastOpts(), nil); err != nil {
			t.Fatal(err)
		}
		assertFile(t, dest, want)
	}
	t.Run("byteranges", func(t *testing.T) { byteranges(t, false) })
	t.Run("byteranges, server ignores Range", func(t *testing.T) { byteranges(t, true) })
}

func encryptedStream(s *server) []byte {
	key := payload(9, 16)
	s.set("/k.key", key)
	plain := payload(1, 4000)
	s.set("/s0.ts", encrypt(key, seqIV(0), plain))
	s.set("/media.m3u8", []byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k.key\"\n#EXTINF:4,\ns0.ts\n#EXT-X-ENDLIST\n"))
	return plain
}

func TestKeyFetchFailures(t *testing.T) {
	t.Run("500 then OK", func(t *testing.T) {
		s := newServer(t)
		want := encryptedStream(s)
		s.setHook(func(w http.ResponseWriter, r *http.Request, hit int) bool {
			if r.URL.Path == "/k.key" && hit <= 3 {
				http.Error(w, "boom", http.StatusInternalServerError)
				return true
			}
			return false
		})
		dest := destPath(t)
		if err := Download(context.Background(), s.URL+"/media.m3u8", dest, fastOpts(), nil); err != nil {
			t.Fatal(err)
		}
		assertFile(t, dest, want)
		if n := s.count("/k.key"); n != 4 {
			t.Fatalf("key requested %d times, want 4", n)
		}
	})
	for name, key := range map[string][]byte{"404": nil, "wrong length": payload(1, 15)} {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			encryptedStream(s)
			s.set("/k.key", key)
			dest := destPath(t)
			err := Download(context.Background(), s.URL+"/media.m3u8", dest, fastOpts(), nil)
			if err == nil || !strings.Contains(err.Error(), "k.key") {
				t.Fatalf("err = %v, want a key error", err)
			}
			if n := s.count("/k.key"); n != 1 {
				t.Fatalf("key requested %d times, want 1", n)
			}
		})
	}
	t.Run("wrong key fails decryption", func(t *testing.T) {
		s := newServer(t)
		encryptedStream(s)
		s.set("/k.key", payload(77, 16))
		err := Download(context.Background(), s.URL+"/media.m3u8", destPath(t), fastOpts(), nil)
		if err == nil || !strings.Contains(err.Error(), "padding") {
			t.Fatalf("err = %v, want a padding error", err)
		}
	})
}

func TestMissingSegment(t *testing.T) {
	s := newServer(t)
	clearStream(s, 8)
	s.set("/s4.ts", nil)
	dest := destPath(t)
	err := Download(context.Background(), s.URL+"/media.m3u8", dest, fastOpts(), nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want a 404", err)
	}
	if n := s.count("/s4.ts"); n != 1 {
		t.Fatalf("missing segment requested %d times, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dest+".hls", "state.json")); err != nil {
		t.Fatalf("work dir not kept: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("dest created: %v", err)
	}
	if err := Discard(dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest + ".hls"); !os.IsNotExist(err) {
		t.Fatalf("Discard left the work dir: %v", err)
	}
}

func TestTransient503(t *testing.T) {
	s := newServer(t)
	want := clearStream(s, 4)
	s.setHook(func(w http.ResponseWriter, r *http.Request, hit int) bool {
		if r.URL.Path == "/s2.ts" && hit <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	})
	dest := destPath(t)
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, fastOpts(), nil); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, want)
	if n := s.count("/s2.ts"); n != 3 {
		t.Fatalf("s2 requested %d times, want 3", n)
	}
}

func TestContentEncodingRejected(t *testing.T) {
	s := newServer(t)
	clearStream(s, 2)
	s.setHook(func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
		}
		if r.URL.Path == "/s1.ts" {
			w.Header().Set("Content-Encoding", "gzip")
		}
		return false
	})
	err := Download(context.Background(), s.URL+"/media.m3u8", destPath(t), fastOpts(), nil)
	if err == nil || !strings.Contains(err.Error(), "Content-Encoding") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlaylistChangeRestarts(t *testing.T) {
	s := newServer(t)
	clearStream(s, 5)
	s.set("/s3.ts", nil)
	dest := destPath(t)
	opts := fastOpts()
	opts.Concurrency = 1 // s0..s2 finish before s3 fails
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, opts, nil); err == nil {
		t.Fatal("first run succeeded")
	}
	if n := s.count("/s0.ts"); n != 1 {
		t.Fatalf("s0 requested %d times", n)
	}
	want := clearStream(s, 6) // the playlist gained a segment
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, opts, nil); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, want)
	for i := range 3 {
		if n := s.count(fmt.Sprintf("/s%d.ts", i)); n != 2 {
			t.Fatalf("s%d requested %d times, want 2 (stale work dir reused)", i, n)
		}
	}
}

func TestStalledSegmentRecovers(t *testing.T) {
	s := newServer(t)
	want := clearStream(s, 3)
	s.setHook(func(w http.ResponseWriter, r *http.Request, hit int) bool {
		if r.URL.Path != "/s1.ts" || hit > 1 {
			return false
		}
		w.Header().Set("Content-Length", "3097")
		w.WriteHeader(http.StatusOK)
		w.Write(want[3000 : 3000+100])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	})
	opts := fastOpts()
	opts.StallTimeout = 200 * time.Millisecond
	dest := destPath(t)
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, opts, nil); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, want)
	if n := s.count("/s1.ts"); n != 2 {
		t.Fatalf("s1 requested %d times, want 2", n)
	}
}

func TestResume(t *testing.T) {
	s := newServer(t)
	const n = 8
	want := clearStream(s, n)
	var block atomic.Bool
	block.Store(true)
	s.setHook(func(w http.ResponseWriter, r *http.Request, _ int) bool {
		var i int
		if _, err := fmt.Sscanf(r.URL.Path, "/s%d.ts", &i); err == nil && i >= 3 && block.Load() {
			<-r.Context().Done()
			return true
		}
		return false
	})
	dest := destPath(t)
	opts := fastOpts()
	opts.Concurrency = 2
	ctx, cancel := context.WithCancel(context.Background())
	err := Download(ctx, s.URL+"/media.m3u8", dest, opts, func(p Progress) {
		if p.SegmentsDone >= 2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first run: %v, want context.Canceled", err)
	}
	var done []int
	for i := range n {
		if _, err := os.Stat(segPath(dest+".hls", i)); err == nil {
			done = append(done, i)
		}
	}
	if len(done) < 2 {
		t.Fatalf("only %v finished before cancel", done)
	}

	block.Store(false)
	var first Progress
	var once sync.Once
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, opts, func(p Progress) {
		once.Do(func() { first = p })
	}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, want)
	for _, i := range done {
		if c := s.count(fmt.Sprintf("/s%d.ts", i)); c != 1 {
			t.Fatalf("finished segment s%d fetched %d times", i, c)
		}
	}
	if first.SegmentsDone != len(done) || first.Completed <= 0 {
		t.Fatalf("resumed progress = %+v, want %d segments done", first, len(done))
	}
}

func TestDestinationExists(t *testing.T) {
	s := newServer(t)
	clearStream(s, 1)
	dest := destPath(t)
	if err := os.WriteFile(dest, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Download(context.Background(), s.URL+"/media.m3u8", dest, fastOpts(), nil)
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("err = %v", err)
	}
	if s.count("/media.m3u8") != 0 {
		t.Fatal("fetched the playlist although dest exists")
	}
}

func TestLocked(t *testing.T) {
	s := newServer(t)
	clearStream(s, 1)
	dest := destPath(t)
	release, err := filelock.Acquire(dest + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, fastOpts(), nil); !errors.Is(err, ErrLocked) {
		t.Fatalf("Download: %v", err)
	}
	if err := Discard(dest); !errors.Is(err, ErrLocked) {
		t.Fatalf("Discard: %v", err)
	}
}

type countingLimiter struct {
	mu            sync.Mutex
	total, maxReq int
}

func (l *countingLimiter) Burst() int { return 1000 }
func (l *countingLimiter) WaitN(_ context.Context, n int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total += n
	l.maxReq = max(l.maxReq, n)
	return nil
}

func TestLimiter(t *testing.T) {
	s := newServer(t)
	want := clearStream(s, 3)
	l := &countingLimiter{}
	opts := fastOpts()
	opts.Limiters = []Limiter{l}
	dest := destPath(t)
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, opts, nil); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, want)
	// The playlist body is throttled too.
	if l.total < len(want) || l.maxReq > 1000 {
		t.Fatalf("limiter saw %d bytes (max %d per wait), want >= %d", l.total, l.maxReq, len(want))
	}
}
