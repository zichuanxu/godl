//go:build darwin

package netproxy

import (
	"net/http"
	"testing"
	"time"
)

func TestReadSystemDarwin(t *testing.T) {
	if _, err := readSystem(); err != nil {
		t.Fatalf("readSystem: %v", err)
	}

	sys.mu.Lock()
	sys.s, sys.at = nil, time.Time{} // force a fresh read
	sys.mu.Unlock()
	t.Cleanup(func() {
		sys.mu.Lock()
		sys.s, sys.at = nil, time.Time{}
		sys.mu.Unlock()
	})

	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	u, err := Func(func() Config { return Config{Mode: ModeSystem} })(req)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil && u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" {
		t.Errorf("unsupported proxy scheme in %v", u)
	}
	t.Logf("system proxy for https://example.com/: %v", u)
}
