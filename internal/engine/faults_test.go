package engine

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// isRange reports a segment request, as opposed to the bytes=0-0 probe.
func isRange(r *http.Request) bool {
	return r.Header.Get("Range") != "" && r.Header.Get("Range") != "bytes=0-0"
}

func TestTransientFaultsAreRetried(t *testing.T) {
	const size = 3 << 20
	data := randomData(t, size)
	for name, fault := range map[string]func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool{
		"truncated body": func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
			if n != 2 {
				return false
			}
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[start : start+1000]) // the server closes the short response
			return true
		},
		"503 with Retry-After": func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
			if n > 3 || !isRange(r) {
				return false
			}
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		},
		"stall mid-body": func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
			if n != 2 {
				return false
			}
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[start : start+10])
			w.(http.Flusher).Flush()
			time.Sleep(time.Second)
			return true
		},
		"slow-loris headers": func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
			if n != 2 {
				return false
			}
			time.Sleep(time.Second)
			return true
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := (&fileServer{data: data, etag: `"v1"`, fault: fault}).start(t)
			cfg := testConfig()
			cfg.StallTimeout = 200 * time.Millisecond
			dest := filepath.Join(t.TempDir(), "out.bin")
			if err := run(t, cfg, server.URL, dest); err != nil {
				t.Fatal(err)
			}
			requireFile(t, dest, data)
		})
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	data := randomData(t, 1<<20)
	server := (&fileServer{data: data, etag: `"v1"`, fault: func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
		if n != 1 {
			return false
		}
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	}}).start(t)
	cfg := testConfig()
	cfg.MaxBackoff = 3 * time.Second
	began := time.Now()
	if err := run(t, cfg, server.URL, filepath.Join(t.TempDir(), "out.bin")); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(began); elapsed < 900*time.Millisecond {
		t.Fatalf("retried after %v; Retry-After asked for 1s", elapsed)
	}
}

func TestProtocolViolationsFail(t *testing.T) {
	for name, tc := range map[string]struct {
		fault func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool
		want  func(error) bool
	}{
		"wrong Content-Range": {
			fault: func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
				if !isRange(r) {
					return false
				}
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Content-Range", "bytes 1-10/3145728")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(make([]byte, 10))
				return true
			},
			want: func(err error) bool { return errors.Is(err, ErrSourceChanged) },
		},
		"range silently ignored": {
			fault: func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
				if !isRange(r) {
					return false
				}
				w.Header().Set("ETag", `"v1"`)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(make([]byte, 3<<20))
				return true
			},
			want: func(err error) bool { return errors.Is(err, ErrRangeUnsupported) },
		},
		"gzip applied to a range": {
			fault: func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
				if !isRange(r) {
					return false
				}
				w.Header().Set("Content-Encoding", "gzip")
				w.WriteHeader(http.StatusPartialContent)
				return true
			},
			want: func(err error) bool { return err != nil && strings.Contains(err.Error(), "Content-Encoding") },
		},
		"not found": {
			fault: func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
				w.WriteHeader(http.StatusNotFound)
				return true
			},
			want: func(err error) bool { return err != nil && strings.Contains(err.Error(), "404") },
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := &fileServer{data: randomData(t, 3<<20), etag: `"v1"`, fault: tc.fault}
			server := srv.start(t)
			err := run(t, testConfig(), server.URL, filepath.Join(t.TempDir(), "out.bin"))
			if !tc.want(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidatorChangeMidDownloadRestartsCleanly(t *testing.T) {
	first, second := randomData(t, 3<<20), randomData(t, 3<<20+17)
	srv := &fileServer{data: first, etag: `"v1"`}
	srv.fault = func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
		if n == 3 {
			srv.replace(second, `"v2"`) // served from now on; If-Range no longer matches
		}
		return false
	}
	server := srv.start(t)
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := run(t, testConfig(), server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, second)
}

func TestMaxAttemptsBoundsPersistentFailures(t *testing.T) {
	srv := &fileServer{data: randomData(t, 1<<20), etag: `"v1"`, fault: func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
		if !isRange(r) {
			return false
		}
		w.WriteHeader(http.StatusBadGateway)
		return true
	}}
	server := srv.start(t)
	cfg := testConfig()
	cfg.Connections = 1
	err := run(t, cfg, server.URL, filepath.Join(t.TempDir(), "out.bin"))
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v; want the 502", err)
	}
	if got := srv.requests.Load(); got > int64(1+cfg.MaxAttempts) {
		t.Fatalf("%d requests; MaxAttempts is %d", got, cfg.MaxAttempts)
	}
}

// A server capping concurrent connections per client answers the extras with
// 503 forever; those connections must retire instead of failing the download.
func TestConnectionCapRetiresExtraConnections(t *testing.T) {
	data := randomData(t, 4<<20)
	srv := &fileServer{data: data, etag: `"v1"`, piece: 64 << 10, pause: time.Millisecond}
	srv.fault = func(w http.ResponseWriter, r *http.Request, start, end, n int64) bool {
		if isRange(r) && srv.active.Load() > 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	server := srv.start(t)
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := run(t, testConfig(), server.URL, dest); err != nil {
		t.Fatal(err)
	}
	requireFile(t, dest, data)
}
