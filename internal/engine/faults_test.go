package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFaultInjectionResponsesAreRejected(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		rangeV   string
		etag     string
		encoding string
		body     string
	}{
		{name: "truncated body", status: http.StatusPartialContent, rangeV: "bytes 0-3/10", etag: `"v1"`, body: "01"},
		{name: "wrong content range", status: http.StatusPartialContent, rangeV: "bytes 1-4/10", etag: `"v1"`, body: "0123"},
		{name: "ignored range", status: http.StatusOK, rangeV: "", etag: `"v1"`, body: "0123456789"},
		{name: "compressed range", status: http.StatusPartialContent, rangeV: "bytes 0-3/10", etag: `"v1"`, encoding: "gzip", body: "0123"},
		{name: "changed etag", status: http.StatusPartialContent, rangeV: "bytes 0-3/10", etag: `"v2"`, body: "0123"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", tt.etag)
				if tt.rangeV != "" {
					w.Header().Set("Content-Range", tt.rangeV)
				}
				if tt.encoding != "" {
					w.Header().Set("Content-Encoding", tt.encoding)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()

			resp, err := newSegmentClient(1).Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if err := validateSegmentResponse(resp, 0, 3, 10, `"v1"`); err == nil {
				t.Fatal("faulty response unexpectedly accepted")
			}
		})
	}
}
