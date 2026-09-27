package engine

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

func newProbeClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	return &http.Client{Transport: transport}
}

func newSegmentClient(workers int) *http.Client {
	if workers < 1 {
		workers = 1
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}
	transport.ForceAttemptHTTP2 = false
	transport.MaxConnsPerHost = workers
	transport.MaxIdleConnsPerHost = workers
	transport.DisableCompression = true
	return &http.Client{Transport: transport}
}
