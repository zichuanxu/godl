package engine

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fileServer serves one representation with configurable validators, range
// support, throttling, and per-request fault hooks.
type fileServer struct {
	mu           sync.Mutex
	data         []byte
	etag         string
	lastModified string
	noRanges     bool // answer every request with 200 and the full body
	ignoreDates  bool // treat a date If-Range as a mismatch
	// piece and pause throttle bodies: pause after every piece bytes.
	piece int
	pause time.Duration
	// fault may take over a range request; it returns true when it handled it.
	fault func(w http.ResponseWriter, r *http.Request, start, end int64, n int64) bool

	requests  atomic.Int64
	sent      atomic.Int64
	active    atomic.Int64
	maxActive atomic.Int64
}

func randomData(t *testing.T, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	r := rand.New(rand.NewPCG(uint64(size), 7))
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	return data
}

func (s *fileServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)
	return server
}

func (s *fileServer) snapshot() ([]byte, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, s.etag, s.lastModified
}

// replace swaps in a new representation, as a server would after an upload.
func (s *fileServer) replace(data []byte, etag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data, s.etag = data, etag
}

func (s *fileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := s.requests.Add(1)
	if a := s.active.Add(1); a > s.maxActive.Load() {
		s.maxActive.Store(a)
	}
	defer s.active.Add(-1)
	data, etag, lastModified := s.snapshot()
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if lastModified != "" {
		w.Header().Set("Last-Modified", lastModified)
	}
	size := int64(len(data))
	start, end, ranged := parseRange(r.Header.Get("Range"), size)
	if ifRange := r.Header.Get("If-Range"); ranged && ifRange != "" {
		matches := ifRange == etag || (!s.ignoreDates && lastModified != "" && ifRange == lastModified)
		ranged = matches
	}
	if s.noRanges || !ranged {
		start, end = 0, size-1
		if s.fault != nil && s.fault(w, r, start, end, n) {
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
		s.write(w, data)
		return
	}
	if s.fault != nil && s.fault(w, r, start, end, n) {
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(http.StatusPartialContent)
	s.write(w, data[start:end+1])
}

func (s *fileServer) write(w http.ResponseWriter, body []byte) {
	piece := s.piece
	if piece <= 0 {
		piece = len(body)
	}
	for len(body) > 0 {
		chunk := body[:min(piece, len(body))]
		n, err := w.Write(chunk)
		s.sent.Add(int64(n))
		if err != nil {
			return
		}
		body = body[len(chunk):]
		if s.pause > 0 && len(body) > 0 {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(s.pause)
		}
	}
}

// parseRange understands the single "bytes=a-b" or "bytes=a-" form.
func parseRange(header string, size int64) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(header, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	first, last, _ := strings.Cut(spec, "-")
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start >= size {
		return 0, 0, false
	}
	end = size - 1
	if last != "" {
		if end, err = strconv.ParseInt(last, 10, 64); err != nil || end < start {
			return 0, 0, false
		}
		end = min(end, size-1)
	}
	return start, end, true
}

// testConfig is a fast configuration for local servers.
func testConfig() Config {
	return Config{
		Connections:        4,
		MinSplitSize:       256 << 10,
		MaxAttempts:        4,
		BaseBackoff:        time.Millisecond,
		MaxBackoff:         20 * time.Millisecond,
		StallTimeout:       2 * time.Second,
		CheckpointInterval: 20 * time.Millisecond,
		ProgressInterval:   10 * time.Millisecond,
	}
}
