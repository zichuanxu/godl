package engine

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func copyFile(t *testing.T, from, to string) bool {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		return false
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return true
}

// cancelAfter runs a download and cancels it once the server has sent at
// least n bytes.
func cancelAfter(t *testing.T, cfg Config, srv *fileServer, url, dest string, n int64) error {
	t.Helper()
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for srv.sent.Load() < n && ctx.Err() == nil {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	return e.Download(ctx, url, dest, nil)
}

func TestResumeAfterCancellationReusesWrittenBytes(t *testing.T) {
	data := randomData(t, 8<<20)
	srv := &fileServer{data: data, etag: `"v1"`, piece: 64 << 10, pause: 2 * time.Millisecond}
	server := srv.start(t)
	dest := filepath.Join(t.TempDir(), "out.bin")

	if err := cancelAfter(t, testConfig(), srv, server.URL, dest, 3<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error = %v; want context.Canceled", err)
	}
	cp, err := readCheckpoint(dest + ".part.meta")
	if err != nil {
		t.Fatalf("no checkpoint after cancellation: %v", err)
	}
	left := totalSize(cp.Remaining)
	if left == 0 || left == int64(len(data)) {
		t.Fatalf("checkpoint claims %d of %d bytes left", left, len(data))
	}

	before := srv.sent.Load()
	if err := run(t, testConfig(), server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, data)
	// Allow the probe byte plus one throttled piece per connection that the
	// server wrote into a socket the client closed after a steal.
	slack := int64(1 + testConfig().Connections*srv.piece)
	if resent := srv.sent.Load() - before; resent > left+slack {
		t.Fatalf("resume transferred %d bytes; the checkpoint had only %d left", resent, left)
	}
}

func TestNoValidatorRestartsFromZeroAcrossSessions(t *testing.T) {
	data := randomData(t, 4<<20)
	srv := &fileServer{data: data, piece: 64 << 10, pause: 2 * time.Millisecond}
	server := srv.start(t)
	dest := filepath.Join(t.TempDir(), "out.bin")
	_ = cancelAfter(t, testConfig(), srv, server.URL, dest, 1<<20)
	if _, err := os.Stat(dest + ".part.meta"); err == nil {
		t.Fatal("a checkpoint was written without any validator")
	}
	before := srv.sent.Load()
	if err := run(t, testConfig(), server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, data)
	if resent := srv.sent.Load() - before; resent < int64(len(data)) {
		t.Fatalf("resumed %d bytes without a validator", resent)
	}
}

// TestRandomizedCrashResume snapshots the partial file and its checkpoint at
// a random moment, as a crash would leave them (metadata first, so the data
// copy holds at least what the metadata claims), then resumes from the
// snapshot and requires a byte-exact result, 100 times.
func TestRandomizedCrashResume(t *testing.T) {
	data := randomData(t, 2<<20)
	srv := &fileServer{data: data, etag: `"v1"`, piece: 32 << 10, pause: 3 * time.Millisecond}
	server := srv.start(t)
	cfg := testConfig()
	cfg.CheckpointInterval = 3 * time.Millisecond
	random := rand.New(rand.NewPCG(42, 99))

	snapshots, midway := 0, 0
	for i := range 100 {
		dir := t.TempDir()
		dest := filepath.Join(dir, "out.bin")
		e, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		// Trigger on progress, not wall time, so slow CI runners still crash
		// mid-download: between 10% and 90% of the file, then one checkpoint.
		target := srv.sent.Load() + int64(float64(len(data))*(0.1+0.8*random.Float64()))
		go func() { done <- e.Download(ctx, server.URL, dest, nil) }()
		for srv.sent.Load() < target && len(done) == 0 {
			time.Sleep(200 * time.Microsecond)
		}
		// Wait for a checkpoint that records progress rather than a fixed
		// time: timer resolution differs by OS (coarse on Windows runners).
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && len(done) == 0; {
			if cp, err := readCheckpoint(dest + ".part.meta"); err == nil && totalSize(cp.Remaining) < int64(len(data)) {
				break
			}
			time.Sleep(time.Millisecond)
		}

		crash := filepath.Join(dir, "crashed.bin")
		taken := copyFile(t, dest+".part.meta", crash+".part.meta") && copyFile(t, dest+".part", crash+".part")
		cancel()
		<-done
		if !taken {
			continue // the download had not started or had already finished
		}
		snapshots++
		if cp, err := readCheckpoint(crash + ".part.meta"); err == nil && totalSize(cp.Remaining) < int64(len(data)) && len(cp.Remaining) > 0 {
			midway++
		}
		if err := run(t, cfg, server.URL, crash); err != nil {
			t.Fatalf("iteration %d: resume from crash snapshot: %v", i, err)
		}
		requireFile(t, crash, data)
	}
	if snapshots < 60 || midway < 50 {
		t.Fatalf("%d snapshots, %d with partial progress; the test is not exercising resume", snapshots, midway)
	}
	t.Logf("%d snapshots, %d with partial progress", snapshots, midway)
}

// TestWorkStealingOvercomesASlowConnection throttles every request that
// starts at offset 0 to 64 KiB/s. Without stealing and replacement the first
// quarter alone would take 16 s.
func TestWorkStealingOvercomesASlowConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a few seconds")
	}
	data := randomData(t, 4<<20)
	srv := &fileServer{data: data, etag: `"v1"`}
	srv.fault = func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
		if start != 0 || !isRange(r) {
			return false
		}
		slow := &fileServer{data: data, etag: `"v1"`, piece: 16 << 10, pause: 250 * time.Millisecond}
		slow.ServeHTTP(w, r)
		return true
	}
	server := srv.start(t)
	cfg := testConfig()
	cfg.StallTimeout = 10 * time.Second
	began := time.Now()
	if err := run(t, cfg, server.URL, filepath.Join(t.TempDir(), "out.bin")); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(began); elapsed > 8*time.Second {
		t.Fatalf("download took %v; the slow connection was not worked around", elapsed)
	}
}
