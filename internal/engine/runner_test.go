package engine

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zichuanxu/nimget/internal/download"
)

func TestRunnerDownloadsWithDynamicRanges(t *testing.T) {
	data := make([]byte, 2<<20)
	for i := range data {
		data[i] = byte(i % 251)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"runner-v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(data)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[:1])
			return
		}
		if rangeHeader == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		start, _ := strconv.Atoi(parts[0])
		end, _ := strconv.Atoi(parts[1])
		body := data[start : end+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	runner, err := NewRunner(Config{Connections: 4, MinSplitSize: 256 << 10, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "runner.bin")
	if err := runner.Download(context.Background(), download.Spec{URL: server.URL, Destination: dest}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("dynamic runner output differs")
	}
	for _, suffix := range []string{".part", ".part.meta", ".lock"} {
		if _, err := os.Stat(dest + suffix); !os.IsNotExist(err) {
			t.Fatalf("temporary file remains: %s", dest+suffix)
		}
	}
}
