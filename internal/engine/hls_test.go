package engine

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zichuanxu/nimget/internal/download"
)

// hlsServer serves an AES-128 encrypted media playlist at /v/index.m3u8 and
// the same playlist at /play (no .m3u8 suffix, detected by Content-Type).
type hlsServer struct {
	segments [][]byte
	key      []byte
	segHits  atomic.Int64
	failFrom atomic.Int64 // segment index from which requests fail with 404; -1 for none
}

func newHLSServer(t *testing.T, n int) (*hlsServer, *httptest.Server) {
	t.Helper()
	s := &hlsServer{key: []byte("0123456789abcdef")}
	s.failFrom.Store(-1)
	for i := range n {
		s.segments = append(s.segments, bytes.Repeat([]byte{byte('a' + i)}, 3000+i))
	}
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)
	return s, server
}

func (s *hlsServer) playlist() string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n")
	for i := range s.segments {
		fmt.Fprintf(&b, "#EXTINF:4.0,\nseg%d.ts\n", i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

func (s *hlsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch path := r.URL.Path; {
	case path == "/v/index.m3u8" || path == "/play":
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(s.playlist()))
	case path == "/v/key.bin" || path == "/key.bin":
		_, _ = w.Write(s.key)
	default:
		var i int
		if _, err := fmt.Sscanf(filepath.Base(path), "seg%d.ts", &i); err != nil || i >= len(s.segments) {
			http.NotFound(w, r)
			return
		}
		s.segHits.Add(1)
		if from := s.failFrom.Load(); from >= 0 && int64(i) >= from {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(encrypt(s.key, i, s.segments[i]))
	}
}

// encrypt applies AES-128-CBC with PKCS#7 padding and the media-sequence IV.
func encrypt(key []byte, seq int, plain []byte) []byte {
	block, _ := aes.NewCipher(key)
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	data := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	iv := make([]byte, aes.BlockSize)
	iv[15] = byte(seq)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(data, data)
	return data
}

func TestHLSPlaylistIsDownloadedAsOneStream(t *testing.T) {
	for _, path := range []string{"/v/index.m3u8", "/play"} {
		srv, server := newHLSServer(t, 5)
		dest := filepath.Join(t.TempDir(), "video.ts")
		var last Progress
		e, err := New(testConfig())
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Download(context.Background(), server.URL+path, dest, func(p Progress) { last = p }); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		if want := bytes.Join(srv.segments, nil); !bytes.Equal(got, want) {
			t.Fatalf("%s: stream differs (%d vs %d bytes)", path, len(got), len(want))
		}
		if last.Completed == 0 {
			t.Fatalf("%s: no progress reported", path)
		}
		for _, leftover := range []string{dest + ".hls", dest + ".part", dest + ".lock"} {
			if _, err := os.Stat(leftover); err == nil {
				t.Errorf("%s: %s left behind", path, leftover)
			}
		}
	}
}

// A failed stream keeps its finished segments; the retry fetches only the
// rest, and Discard removes the partial state.
func TestHLSResumesAndDiscards(t *testing.T) {
	srv, server := newHLSServer(t, 6)
	srv.failFrom.Store(3)
	dir := t.TempDir()
	dest := filepath.Join(dir, "video.ts")
	cfg := testConfig()
	cfg.Connections = 1 // segments in order, so exactly 0-2 succeed
	cfg.MaxAttempts = 1
	runner, err := NewRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	spec := download.Spec{URL: server.URL + "/v/index.m3u8", Destination: dest}
	if err := runner.Download(context.Background(), spec, nil); err == nil {
		t.Fatal("download with a missing segment succeeded")
	}
	before := srv.segHits.Load()
	srv.failFrom.Store(-1)
	if err := runner.Download(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	if refetched := srv.segHits.Load() - before; refetched != 3 {
		t.Fatalf("resume fetched %d segments, want the 3 missing ones", refetched)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, bytes.Join(srv.segments, nil)) {
		t.Fatal("resumed stream differs")
	}

	other := filepath.Join(dir, "other.ts")
	srv.failFrom.Store(2)
	_ = runner.Download(context.Background(), download.Spec{URL: server.URL + "/v/index.m3u8", Destination: other}, nil)
	if _, err := os.Stat(other + ".hls"); err != nil {
		t.Fatalf("partial segment directory missing: %v", err)
	}
	if err := runner.Discard(other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other + ".hls"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Discard kept the segment directory: %v", err)
	}
}

// A playlist URL whose destination folder does not exist yet (the manager's
// category folders) is created; a redirect to an .m3u8 served with a generic
// type is still treated as a stream.
func TestHLSIntoNewFolderAndThroughRedirect(t *testing.T) {
	srv, server := newHLSServer(t, 3)
	want := bytes.Join(srv.segments, nil)
	e, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "Video", "new", "clip.ts")
	if err := e.Download(context.Background(), server.URL+"/v/index.m3u8", dest, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, want) {
		t.Fatal("stream differs")
	}

	mux := http.NewServeMux()
	generic := httptest.NewServer(mux)
	defer generic.Close()
	srv2, server2 := newHLSServer(t, 2)
	mux.HandleFunc("/v/plain.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(strings.ReplaceAll(srv2.playlist(), "key.bin", server2.URL+"/key.bin")))
	})
	mux.HandleFunc("/v/", func(w http.ResponseWriter, r *http.Request) { srv2.ServeHTTP(w, r) })
	mux.HandleFunc("/watch2", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v/plain.m3u8", http.StatusFound)
	})
	dest2 := filepath.Join(t.TempDir(), "watch.ts")
	if err := e.Download(context.Background(), generic.URL+"/watch2", dest2, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest2); !bytes.Equal(got, bytes.Join(srv2.segments, nil)) {
		t.Fatalf("redirected playlist saved as %q…", got[:min(20, len(got))])
	}
}
