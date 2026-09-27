package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// A rate limit slows the download without tripping the stall timeout, whose
// clock must not run while a read waits on a limiter.
func TestRateLimitCapsThroughputWithoutStalling(t *testing.T) {
	for _, noRanges := range []bool{false, true} {
		data := randomData(t, 768<<10)
		srv := &fileServer{data: data, etag: `"v1"`, noRanges: noRanges}
		server := srv.start(t)
		cfg := testConfig()
		cfg.MaxAttempts = 1
		cfg.StallTimeout = 100 * time.Millisecond
		// 256 KiB burst, then 512 KiB at 1 MiB/s: about 0.5 s, with single
		// waits of 250 ms, longer than the stall timeout.
		cfg.Limiters = []*rate.Limiter{rate.NewLimiter(rate.Inf, bufferSize), rate.NewLimiter(1<<20, bufferSize)}
		dest := filepath.Join(t.TempDir(), "limited.bin")
		began := time.Now()
		if err := run(t, cfg, server.URL, dest); err != nil {
			t.Fatalf("noRanges=%v: %v", noRanges, err)
		}
		if elapsed := time.Since(began); elapsed < 400*time.Millisecond {
			t.Fatalf("noRanges=%v: limited download took %v, want >= 400ms", noRanges, elapsed)
		}
		requireFile(t, dest, data)
	}
}

func TestInspectReportsDispositionAndFinalURL(t *testing.T) {
	srv := &fileServer{data: randomData(t, 1000), etag: `"v1"`}
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/files/real.bin", http.StatusFound)
	})
	mux.HandleFunc("/files/real.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="fallback.txt"; filename*=UTF-8''na%C3%AFve%20file.txt`)
		w.Header().Set("Content-Type", "text/plain")
		srv.ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	e, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := e.Inspect(context.Background(), server.URL+"/start")
	if err != nil {
		t.Fatal(err)
	}
	if remote.Filename != "naïve file.txt" || remote.URL != server.URL+"/files/real.bin" || remote.Size != 1000 || remote.ContentType != "text/plain" {
		t.Fatalf("remote = %+v", remote)
	}
}

// Headers the caller set stay with the original host across a redirect.
func TestRedirectToAnotherHostDropsCustomHeaders(t *testing.T) {
	var got http.Header
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Range", "bytes 0-0/1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("x"))
	}))
	defer other.Close()
	// 127.0.0.1 and localhost are different hosts to the redirect check.
	target := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/f", http.StatusFound)
	}))
	defer origin.Close()
	cfg := testConfig()
	cfg.Headers = http.Header{"X-Api-Key": {"secret"}, "Referer": {"https://example.com/"}}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Inspect(context.Background(), origin.URL+"/f"); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Api-Key") != "" || got.Get("Referer") == "" {
		t.Fatalf("redirected request headers = %v", got)
	}
}

func TestDispositionNameIsLenient(t *testing.T) {
	for header, want := range map[string]string{
		`attachment; filename="a b.zip"`:             "a b.zip",
		`attachment; filename=my file.zip`:           "my file.zip",
		`attachment; filename*=UTF-8''%E2%82%AC.txt`: "€.txt",
		`inline`: "",
	} {
		if got := dispositionName(header); got != want {
			t.Errorf("dispositionName(%q) = %q, want %q", header, got, want)
		}
	}
}
