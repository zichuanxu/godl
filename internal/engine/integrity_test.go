package engine

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateSegmentResponseRejectsProtocolViolations(t *testing.T) {
	data := []byte("0123456789")
	tests := []struct {
		name   string
		status int
		rangeV string
		etag   string
		body   []byte
	}{
		{name: "wrong status", status: http.StatusOK, rangeV: "bytes 0-3/10", etag: `"v1"`, body: data[:4]},
		{name: "wrong range", status: http.StatusPartialContent, rangeV: "bytes 1-4/10", etag: `"v1"`, body: data[:4]},
		{name: "changed etag", status: http.StatusPartialContent, rangeV: "bytes 0-3/10", etag: `"v2"`, body: data[:4]},
		{name: "short body", status: http.StatusPartialContent, rangeV: "bytes 0-3/10", etag: `"v1"`, body: data[:3]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", tt.etag)
				w.Header().Set("Content-Range", tt.rangeV)
				w.Header().Set("Content-Length", fmt.Sprint(len(tt.body)))
				w.WriteHeader(tt.status)
				_, _ = w.Write(tt.body)
			}))
			defer server.Close()

			resp, err := newSegmentClient(1).Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if err := validateSegmentResponse(resp, 0, 3, 10, `"v1"`); err == nil {
				t.Fatal("invalid segment response unexpectedly accepted")
			}
		})
	}
}

func TestValidateSegmentResponseAcceptsExactResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := []byte("0123")
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Range", "bytes 0-3/10")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	resp, err := newSegmentClient(1).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := validateSegmentResponse(resp, 0, 3, 10, `"v1"`); err != nil {
		t.Fatal(err)
	}
	body, err := readExactResponse(resp.Body, 4)
	if err != nil || !bytes.Equal(body, []byte("0123")) {
		t.Fatalf("body = %q, err = %v", body, err)
	}
}
