// Package netproxy selects the proxy for outgoing HTTP requests: none, a manual
// HTTP/HTTPS/SOCKS5 proxy, or the operating system's proxy settings.
package netproxy

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Mode selects where proxy settings come from.
type Mode string

const (
	ModeSystem Mode = "system" // default when empty
	ModeNone   Mode = "none"
	ModeManual Mode = "manual"
)

// Config is the user-facing proxy configuration.
type Config struct {
	Mode Mode   `json:"mode,omitempty"`
	URL  string `json:"url,omitempty"` // manual only: http://, https://, or socks5:// with optional user:pass@
}

// Validate rejects unknown modes and malformed or unsupported manual URLs.
func (c Config) Validate() error {
	switch c.Mode {
	case "", ModeSystem, ModeNone:
		return nil
	case ModeManual:
		_, err := parseManual(c.URL)
		return err
	}
	return fmt.Errorf("netproxy: unknown mode %q", c.Mode)
}

// Func returns an http.Transport.Proxy function. It calls get on every request, so
// configuration changes apply to new connections without rebuilding transports.
func Func(get func() Config) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if isLoopback(req.URL.Hostname()) {
			return nil, nil
		}
		c := get()
		switch c.Mode {
		case ModeNone:
			return nil, nil
		case ModeManual:
			return parseManual(c.URL)
		case "", ModeSystem:
			if s := systemSettings(); s != nil {
				return s.proxyFor(req.URL), nil
			}
			return http.ProxyFromEnvironment(req)
		}
		return nil, fmt.Errorf("netproxy: unknown mode %q", c.Mode)
	}
}

func parseManual(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("netproxy: invalid proxy URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("netproxy: unsupported proxy scheme %q (want http, https, or socks5)", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("netproxy: proxy URL %q has no host", raw)
	}
	return u, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

const cacheTTL = 30 * time.Second

// sys caches the OS proxy settings; a nil s means "no system proxy, use the environment".
var sys struct {
	mu sync.Mutex
	at time.Time
	s  *settings
}

// systemSettings returns the cached OS settings, re-reading them after cacheTTL.
func systemSettings() *settings {
	sys.mu.Lock()
	defer sys.mu.Unlock()
	if !sys.at.IsZero() && time.Since(sys.at) < cacheTTL {
		return sys.s
	}
	s, err := readSystem()
	if err != nil {
		s = nil // unreadable: fall back to the environment
	}
	sys.s, sys.at = s, time.Now()
	return s
}
