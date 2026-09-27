package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/client"
	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/service"
)

const mainEnv = "GODL_TEST_RUN_MAIN"

// TestMain lets the test binary act as the godl executable in child processes.
func TestMain(m *testing.M) {
	if os.Getenv(mainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func godl(args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), mainEnv+"=1")
	return cmd
}

// throttledServer serves a deterministic file with ranges and a strong ETag,
// at about 2 MiB/s per connection, and counts the body bytes it sends.
func throttledServer(t *testing.T, data []byte, sent *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"e2e-v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		start, end := 0, len(data)-1
		status := http.StatusOK
		if spec, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes="); ok {
			first, last, _ := strings.Cut(spec, "-")
			start, _ = strconv.Atoi(first)
			if last != "" {
				end, _ = strconv.Atoi(last)
			}
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		}
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(status)
		const piece = 256 << 10
		for offset := start; offset <= end; offset += piece {
			stop := min(offset+piece, end+1)
			n, err := w.Write(data[offset:stop])
			sent.Add(int64(n))
			if err != nil {
				return
			}
			if stop-offset == piece {
				time.Sleep(125 * time.Millisecond)
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

type serviceProcess struct {
	cmd    *exec.Cmd
	client *client.Client
}

func startServiceProcess(t *testing.T, dir, root string) *serviceProcess {
	t.Helper()
	tokenPath := filepath.Join(dir, "token")
	cmd := godl("service",
		"--listen", "127.0.0.1:0",
		"--database", filepath.Join(dir, "godl.db"),
		"--token-file", tokenPath,
		"--download-root", root,
		"--log-level", "error",
	)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("service did not report its address: %v", err)
	}
	address, ok := strings.CutPrefix(strings.TrimSpace(line), "godl service listening on ")
	if !ok {
		t.Fatalf("unexpected service output %q", line)
	}
	token, err := service.ReadToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	return &serviceProcess{cmd: cmd, client: client.New("http://"+address, token, nil)}
}

func (p *serviceProcess) item(t *testing.T) download.Item {
	t.Helper()
	items, err := p.client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v; want exactly one", items)
	}
	return items[0]
}

func TestServiceResumesAfterKill(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end crash test takes several seconds")
	}
	// Eight connections at about 2 MiB/s each need about 8 s for 128 MiB, so
	// half the file is written and checkpointed well before the kill.
	data := make([]byte, 128<<20)
	for i := range data {
		data[i] = byte(i % 251)
	}
	var sent atomic.Int64
	server := throttledServer(t, data, &sent)

	dir := t.TempDir()
	root := filepath.Join(dir, "downloads")
	dest := filepath.Join(root, "big.bin")

	first := startServiceProcess(t, dir, root)
	if _, err := first.client.Add(context.Background(), download.Request{URL: server.URL + "/big.bin", Destination: dest}); err != nil {
		t.Fatal(err)
	}
	// The server's byte count is a precise, poll-free signal of progress.
	deadline := time.Now().Add(30 * time.Second)
	for sent.Load() < 64<<20 {
		if time.Now().After(deadline) {
			t.Fatalf("half the file was not sent: %+v", first.item(t))
		}
		time.Sleep(50 * time.Millisecond)
	}
	halfway := time.Now()
	// Checkpoints are written every 2 s; wait for one after the halfway mark.
	for {
		info, err := os.Stat(dest + ".part.meta")
		if err == nil && info.ModTime().After(halfway) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no checkpoint after the halfway mark: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if item := first.item(t); item.Status != download.StatusRunning {
		t.Fatalf("download no longer running before the kill: %+v", item)
	}
	if err := first.cmd.Process.Kill(); err != nil { // SIGKILL / TerminateProcess
		t.Fatal(err)
	}
	_ = first.cmd.Wait()
	sentBeforeKill := sent.Load()

	second := startServiceProcess(t, dir, root)
	deadline = time.Now().Add(60 * time.Second)
	for {
		item := second.item(t)
		if item.Status == download.StatusCompleted {
			break
		}
		if item.Status == download.StatusFailed || time.Now().After(deadline) {
			t.Fatalf("download after restart = %+v", item)
		}
		time.Sleep(100 * time.Millisecond)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("resumed file differs from the source")
	}
	if sentBeforeKill >= int64(len(data)) {
		t.Fatalf("the whole file was sent before the kill; the test did not interrupt anything")
	}
	resent := sent.Load() - sentBeforeKill
	if resent >= int64(len(data)) {
		t.Fatalf("restart re-downloaded %d of %d bytes; checkpointed chunks were not reused", resent, len(data))
	}
	t.Logf("sent %d bytes before the kill, %d after restart (file is %d)", sentBeforeKill, resent, len(data))
	for _, suffix := range []string{".part", ".part.meta", ".lock"} {
		if _, err := os.Stat(dest + suffix); err == nil {
			t.Errorf("temporary file %s remains", dest+suffix)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	out, err := godl("version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "godl dev (commit none") {
		t.Fatalf("version output = %q, %v", out, err)
	}
}

func TestParseSpeedAndPriority(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "500": 500, "500K": 500 << 10, "2m": 2 << 20, "1.5M": 3 << 19, "1G/s": 1 << 30, "4KB": 4096} {
		if got, err := parseSpeed(in); err != nil || got != want {
			t.Errorf("parseSpeed(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"-1", "fast", "2T"} {
		if _, err := parseSpeed(bad); err == nil {
			t.Errorf("parseSpeed(%q) accepted", bad)
		}
	}
	if p, err := parsePriority("HIGH"); err != nil || p != 1 {
		t.Fatalf("parsePriority = %d, %v", p, err)
	}
	if _, err := parsePriority("urgent"); err == nil {
		t.Fatal("unknown priority accepted")
	}
}
