package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zichuanxu/nimget/internal/filelock"
)

func run(t *testing.T, cfg Config, url, dest string) error {
	t.Helper()
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e.Download(context.Background(), url, dest, nil)
}

func requireFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the source (%d vs %d bytes)", path, len(got), len(want))
	}
	for _, suffix := range []string{".part", ".part.meta", ".lock"} {
		if _, err := os.Stat(path + suffix); err == nil {
			t.Errorf("temporary file %s remains", path+suffix)
		}
	}
}

func TestParallelDownloadUsesSeveralConnections(t *testing.T) {
	data := randomData(t, 8<<20)
	srv := &fileServer{data: data, etag: `"v1"`, piece: 64 << 10, pause: 2e6}
	server := srv.start(t)
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := run(t, testConfig(), server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, data)
	if got := srv.maxActive.Load(); got < 3 {
		t.Fatalf("at most %d concurrent requests; want parallel connections", got)
	}
}

func TestDownloadModes(t *testing.T) {
	data := randomData(t, 3<<20)
	for name, srv := range map[string]*fileServer{
		"no ranges":          {data: data, etag: `"v1"`, noRanges: true},
		"last-modified only": {data: data, lastModified: "Sun, 27 Sep 2026 12:00:00 GMT"},
		"date If-Range ignored": {
			data: data, lastModified: "Sun, 27 Sep 2026 12:00:00 GMT", ignoreDates: true,
		},
		"no validator": {data: data},
		"weak etag":    {data: data, etag: `W/"v1"`},
	} {
		t.Run(name, func(t *testing.T) {
			server := srv.start(t)
			dest := filepath.Join(t.TempDir(), "out.bin")
			if err := run(t, testConfig(), server.URL, dest); err != nil {
				t.Fatal(err)
			}
			requireFile(t, dest, data)
		})
	}
}

func TestSmallAndEmptyFiles(t *testing.T) {
	for _, size := range []int{0, 1, 1000, 300 << 10} {
		data := randomData(t, size)
		server := (&fileServer{data: data, etag: `"v1"`}).start(t)
		dest := filepath.Join(t.TempDir(), "out.bin")
		if err := run(t, testConfig(), server.URL, dest); err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		requireFile(t, dest, data)
	}
}

func TestChecksumVerification(t *testing.T) {
	data := randomData(t, 1<<20)
	server := (&fileServer{data: data, etag: `"v1"`}).start(t)
	sum := sha256.Sum256(data)

	cfg := testConfig()
	cfg.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	dest := filepath.Join(t.TempDir(), "ok.bin")
	if err := run(t, cfg, server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, data)

	cfg.Checksum = "sha256:" + hex.EncodeToString(make([]byte, 32))
	bad := filepath.Join(t.TempDir(), "bad.bin")
	if err := run(t, cfg, server.URL, bad); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("wrong checksum error = %v; want ErrChecksumMismatch", err)
	}
	for _, path := range []string{bad, bad + ".part", bad + ".part.meta"} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s kept after a checksum mismatch", path)
		}
	}
	for _, invalid := range []string{"sha256:zz", "crc32:00", "sha1", "md5:" + hex.EncodeToString(make([]byte, 15))} {
		if _, err := New(Config{Checksum: invalid}); err == nil {
			t.Errorf("checksum %q accepted", invalid)
		}
	}
}

func TestDestinationRulesAndLock(t *testing.T) {
	data := randomData(t, 1000)
	server := (&fileServer{data: data, etag: `"v1"`}).start(t)
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(t, testConfig(), server.URL, dest); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("existing destination error = %v", err)
	}
	cfg := testConfig()
	cfg.Overwrite = true
	if err := run(t, cfg, server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, data)

	locked := filepath.Join(t.TempDir(), "locked.bin")
	release, err := filelock.Acquire(locked + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := run(t, testConfig(), server.URL, locked); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked destination error = %v; want ErrLocked", err)
	}
}

func TestSchedulerPreSplitsStealsAndAccounts(t *testing.T) {
	const size, minSplit = 16 << 20, 1 << 20
	s := newScheduler([]interval{{0, size}}, minSplit, 4)
	if len(s.pending) != 4 {
		t.Fatalf("pre-split into %d intervals; want 4", len(s.pending))
	}
	var segs []*segment
	for range 4 {
		segs = append(segs, s.next())
	}
	// Finish the first segment; its worker then steals half of the largest.
	segs[0].advance(segs[0].remaining())
	s.release(segs[0])
	segs[1].advance(1 << 20) // 3 MiB left
	stolen := s.next()
	if stolen == nil {
		t.Fatal("no work stolen from active segments")
	}
	if stolen.end-stolen.cursor < minSplit {
		t.Fatalf("stolen range %d-%d is below the minimum split", stolen.cursor, stolen.end)
	}
	if got := totalSize(s.remaining()); got != size-4<<20-1<<20 {
		t.Fatalf("remaining = %d bytes; want %d", got, size-4<<20-1<<20)
	}
	for _, iv := range s.remaining() {
		if iv.size() <= 0 {
			t.Fatalf("empty interval in %+v", s.remaining())
		}
	}
	// Releasing a failed segment returns its unwritten bytes.
	s.release(stolen)
	if got := totalSize(s.remaining()); got != size-4<<20-1<<20 {
		t.Fatalf("remaining after release = %d", got)
	}
}

func TestCheckpointValidationAndResumability(t *testing.T) {
	good := checkpoint{Version: checkpointVersion, Identity: "id", Size: 100, ETag: `"v1"`, Remaining: []interval{{0, 10}, {50, 100}}}
	path := filepath.Join(t.TempDir(), "meta")
	if err := writeCheckpoint(path, good); err != nil {
		t.Fatal(err)
	}
	got, err := readCheckpoint(path)
	if err != nil || !got.resumable(good) {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	for name, bad := range map[string][]interval{
		"overlap":      {{0, 60}, {50, 100}},
		"out of range": {{90, 101}},
		"empty":        {{5, 5}},
	} {
		c := good
		c.Remaining = bad
		if err := c.validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	noValidator := good
	noValidator.ETag = ""
	if noValidator.resumable(noValidator) {
		t.Error("a checkpoint without validators must not resume")
	}
	other := good
	other.ETag = `"v2"`
	if good.resumable(other) {
		t.Error("a checkpoint for another ETag must not resume")
	}
}
