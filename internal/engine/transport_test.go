package engine

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestSegmentClientForcesHTTP11(t *testing.T) {
	client := newSegmentClient(6, nil)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("segment transport attempts HTTP/2")
	}
	if transport.MaxConnsPerHost != 6 || transport.MaxIdleConnsPerHost != 6 {
		t.Fatalf("connection limits = %d/%d", transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost)
	}
	if transport.TLSClientConfig == nil || len(transport.TLSClientConfig.NextProtos) != 1 || transport.TLSClientConfig.NextProtos[0] != "http/1.1" {
		t.Fatalf("TLS NextProtos = %+v", transport.TLSClientConfig)
	}
	if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("minimum TLS version = %d", transport.TLSClientConfig.MinVersion)
	}
}
