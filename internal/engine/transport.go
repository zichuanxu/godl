package engine

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type proxyFunc = func(*http.Request) (*url.URL, error)

func newProbeClient(proxy proxyFunc) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = proxy
	transport.DisableCompression = true
	return &http.Client{Transport: transport, CheckRedirect: checkRedirect}
}

// portableHeaders may follow a redirect to another host; every other header
// the caller set (API keys, site headers) stays with the original host. Go
// itself only strips Authorization and Cookie.
var portableHeaders = map[string]bool{
	"User-Agent": true, "Accept": true, "Accept-Language": true, "Accept-Encoding": true,
	"Range": true, "If-Range": true, "If-Match": true, "Referer": true,
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
		for name := range req.Header {
			if !portableHeaders[name] {
				req.Header.Del(name)
			}
		}
	}
	return nil
}

func newSegmentClient(workers int, proxy proxyFunc) *http.Client {
	if workers < 1 {
		workers = 1
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = proxy
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}
	transport.ForceAttemptHTTP2 = false
	transport.MaxConnsPerHost = workers
	transport.MaxIdleConnsPerHost = workers
	transport.DisableCompression = true
	return &http.Client{Transport: transport, CheckRedirect: checkRedirect}
}
