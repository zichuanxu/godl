package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/filelock"
)

type rangeServer struct {
	data []byte
	etag string

	delay time.Duration

	active    atomic.Int64
	maxActive atomic.Int64
	ranges    atomic.Int64
	full      atomic.Int64

	mu        sync.Mutex
	failStart map[int64]int
	starts    map[int64]int
}

func newRangeServer(data []byte, etag string) *rangeServer {
	return &rangeServer{
		data:      data,
		etag:      etag,
		failStart: make(map[int64]int),
		starts:    make(map[int64]int),
	}
}

func (s *rangeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.etag != "" {
		w.Header().Set("ETag", s.etag)
	}
	w.Header().Set("Accept-Ranges", "bytes")

	if match := r.Header.Get("If-Match"); match != "" && match != s.etag {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		s.full.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(s.data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(s.data)
		return
	}

	s.ranges.Add(1)
	start, end, ok := parseRequestRange(rangeHeader, int64(len(s.data)))
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(s.data)))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if ifRange := r.Header.Get("If-Range"); ifRange != "" && ifRange != s.etag {
		w.Header().Set("Content-Length", strconv.Itoa(len(s.data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(s.data)
		return
	}

	s.mu.Lock()
	s.starts[start]++
	remainingFailures := s.failStart[start]
	if remainingFailures > 0 {
		s.failStart[start] = remainingFailures - 1
	}
	s.mu.Unlock()
	if remainingFailures > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	active := s.active.Add(1)
	for {
		old := s.maxActive.Load()
		if active <= old || s.maxActive.CompareAndSwap(old, active) {
			break
		}
	}
	defer s.active.Add(-1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}

	body := s.data[start : end+1]
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(s.data)))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(body)
}

func parseRequestRange(value string, size int64) (start, end int64, ok bool) {
	if !strings.HasPrefix(value, "bytes=") {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "bytes="), "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err1 := strconv.ParseInt(parts[0], 10, 64)
	end, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || start < 0 || end < start || end >= size {
		return 0, 0, false
	}
	return start, end, true
}

func testData(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte((i*31 + 7) % 251)
	}
	return data
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestParallelDownload(t *testing.T) {
	data := testData(3 << 20)
	rs := newRangeServer(data, `"v1"`)
	rs.delay = 15 * time.Millisecond
	server := httptest.NewServer(rs)
	defer server.Close()

	dl, err := New(Config{
		Workers:            4,
		ChunkSize:          256 << 10,
		MinParallelSize:    1,
		MaxAttempts:        2,
		CheckpointInterval: 5 * time.Millisecond,
		ExpectedSHA256:     sha256Hex(data),
	})
	if err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "file.bin")
	var last Progress
	if err := dl.Download(context.Background(), server.URL, dest, func(p Progress) { last = p }); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded bytes differ")
	}
	if rs.maxActive.Load() < 2 {
		t.Fatalf("expected concurrent requests, max active = %d", rs.maxActive.Load())
	}
	if last.Completed != int64(len(data)) || last.Total != int64(len(data)) {
		t.Fatalf("unexpected final progress: %+v", last)
	}
	for _, suffix := range []string{".part", ".part.meta", ".lock"} {
		if _, err := os.Stat(dest + suffix); !os.IsNotExist(err) {
			t.Fatalf("temporary file remains: %s", dest+suffix)
		}
	}
}

func TestWeakOrMissingETagFallsBackToSingleStream(t *testing.T) {
	data := testData(1 << 20)
	rs := newRangeServer(data, "")
	server := httptest.NewServer(rs)
	defer server.Close()

	dl, err := New(Config{
		Workers:         4,
		ChunkSize:       128 << 10,
		MinParallelSize: 1,
		MaxAttempts:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "single.bin")
	if err := dl.Download(context.Background(), server.URL, dest, nil); err != nil {
		t.Fatal(err)
	}
	if rs.ranges.Load() != 1 { // only the 0-0 capability probe
		t.Fatalf("expected one probe range request, got %d", rs.ranges.Load())
	}
	if rs.full.Load() != 1 {
		t.Fatalf("expected one full request, got %d", rs.full.Load())
	}
}

func TestResumeAfterFailure(t *testing.T) {
	const chunkSize = int64(128 << 10)
	data := testData(4 << 20)
	rs := newRangeServer(data, `"resume-v1"`)
	rs.delay = 8 * time.Millisecond
	// The first request for a later chunk fails, leaving earlier chunks available
	// for a checkpoint.
	rs.failStart[12*chunkSize] = 1
	server := httptest.NewServer(rs)
	defer server.Close()

	dl, err := New(Config{
		Workers:            4,
		ChunkSize:          chunkSize,
		MinParallelSize:    1,
		MaxAttempts:        1,
		CheckpointInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "resume.bin")
	if err := dl.Download(context.Background(), server.URL, dest, nil); err == nil {
		t.Fatal("first download unexpectedly succeeded")
	}
	if _, err := os.Stat(dest + ".part.meta"); err != nil {
		t.Fatalf("resume metadata not retained: %v", err)
	}

	rs.mu.Lock()
	before := make(map[int64]int, len(rs.starts))
	for k, v := range rs.starts {
		before[k] = v
	}
	rs.mu.Unlock()

	if err := dl.Download(context.Background(), server.URL, dest, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("resumed download differs")
	}

	// At least one successfully checkpointed data chunk must not be fetched again.
	skipped := false
	rs.mu.Lock()
	for start, countBefore := range before {
		if start == 0 { // 0-0 probes also use start 0, so avoid ambiguity.
			continue
		}
		if countBefore > 0 && rs.starts[start] == countBefore {
			skipped = true
			break
		}
	}
	rs.mu.Unlock()
	if !skipped {
		t.Fatal("no completed chunk was reused from resume metadata")
	}
}

func TestDestinationLock(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "locked.bin")
	release, err := filelock.Acquire(dest + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	dl, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	err = dl.Download(context.Background(), "https://example.invalid/file", dest, nil)
	if !errorsIs(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
}

// errorsIs keeps this test file's imports compact while still checking wrapping.
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestSourceChangeTriggersCleanRestart(t *testing.T) {
	dataV1 := testData(2 << 20)
	dataV2 := append([]byte(nil), dataV1...)
	for i := range dataV2 {
		dataV2[i] ^= 0x5a
	}

	var version atomic.Int64
	version.Store(1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := version.Load()
		data := dataV1
		etag := `"v1"`
		if current == 2 {
			data = dataV2
			etag = `"v2"`
		}

		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "bytes=0-0" && r.Header.Get("If-Range") == "" {
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(data)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[:1])
			return
		}

		// Change the representation as soon as the first v1 data range arrives.
		if rangeHeader != "" && r.Header.Get("If-Range") == `"v1"` {
			version.CompareAndSwap(1, 2)
			data = dataV2
			etag = `"v2"`
		}
		w.Header().Set("ETag", etag)
		if rangeHeader == "" || (r.Header.Get("If-Range") != "" && r.Header.Get("If-Range") != etag) {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		start, end, ok := parseRequestRange(rangeHeader, int64(len(data)))
		if !ok {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(data)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		body := data[start : end+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	dl, err := New(Config{
		Workers:         4,
		ChunkSize:       128 << 10,
		MinParallelSize: 1,
		MaxAttempts:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "changed.bin")
	if err := dl.Download(context.Background(), server.URL, dest, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dataV2) {
		t.Fatal("download did not restart on the new representation")
	}
}
