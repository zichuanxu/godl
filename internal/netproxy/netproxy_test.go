package netproxy

import (
	"net/http"
	"testing"
	"time"
)

// useSystem pins the system-settings cache to s for the duration of the test.
func useSystem(t *testing.T, s *settings) {
	t.Helper()
	sys.mu.Lock()
	sys.s, sys.at = s, time.Now()
	sys.mu.Unlock()
	t.Cleanup(func() {
		sys.mu.Lock()
		sys.s, sys.at = nil, time.Time{}
		sys.mu.Unlock()
	})
}

func proxyString(t *testing.T, cfg Config, target string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := Func(func() Config { return cfg })(req)
	if err != nil {
		t.Fatalf("Func(%+v)(%s): %v", cfg, target, err)
	}
	if u == nil {
		return ""
	}
	return u.String()
}

func TestValidate(t *testing.T) {
	ok := []Config{
		{},
		{Mode: ModeSystem},
		{Mode: ModeNone},
		{Mode: ModeManual, URL: "http://proxy:3128"},
		{Mode: ModeManual, URL: "https://user:pass@proxy.example.com"},
		{Mode: ModeManual, URL: "socks5://127.0.0.1:1080"},
		{Mode: ModeManual, URL: "SOCKS5://[::1]:1080"},
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", c, err)
		}
	}
	bad := []Config{
		{Mode: "pac"},
		{Mode: ModeManual},
		{Mode: ModeManual, URL: "proxy:3128"},
		{Mode: ModeManual, URL: "ftp://proxy:21"},
		{Mode: ModeManual, URL: "socks4://proxy:1080"},
		{Mode: ModeManual, URL: "http://"},
		{Mode: ModeManual, URL: "http://proxy:port"},
	}
	for _, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want error", c)
		}
	}
}

func TestFuncModes(t *testing.T) {
	useSystem(t, &settings{
		http:  proxyURL("http", "sys-http", "8080"),
		https: proxyURL("http", "sys-https", "8443"),
		socks: proxyURL("socks5", "sys-socks", "1080"),
	})
	manual := Config{Mode: ModeManual, URL: "socks5://u:p@proxy:1080"}
	tests := []struct {
		cfg    Config
		target string
		want   string
	}{
		{Config{Mode: ModeNone}, "http://example.com/", ""},
		{manual, "http://example.com/", "socks5://u:p@proxy:1080"},
		{manual, "https://example.com/", "socks5://u:p@proxy:1080"},
		{Config{}, "http://example.com/", "http://sys-http:8080"},
		{Config{Mode: ModeSystem}, "https://example.com/", "http://sys-https:8443"},
		{Config{Mode: ModeSystem}, "ftp://example.com/", "socks5://sys-socks:1080"},
		// Loopback always bypasses.
		{manual, "http://localhost:8080/", ""},
		{manual, "http://LOCALHOST/", ""},
		{manual, "http://127.0.0.1:1234/", ""},
		{manual, "http://127.5.6.7/", ""},
		{manual, "http://[::1]:80/", ""},
		{Config{}, "https://127.0.0.1/", ""},
	}
	for _, tt := range tests {
		if got := proxyString(t, tt.cfg, tt.target); got != tt.want {
			t.Errorf("mode %q %s: got %q, want %q", tt.cfg.Mode, tt.target, got, tt.want)
		}
	}
}

func TestFuncErrors(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	for _, c := range []Config{{Mode: "bogus"}, {Mode: ModeManual, URL: "ftp://x"}} {
		if _, err := Func(func() Config { return c })(req); err == nil {
			t.Errorf("Func(%+v) returned no error", c)
		}
	}
}

func TestFuncReadsConfigPerRequest(t *testing.T) {
	cfg := Config{Mode: ModeNone}
	f := Func(func() Config { return cfg })
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if u, _ := f(req); u != nil {
		t.Fatalf("got %v, want nil", u)
	}
	cfg = Config{Mode: ModeManual, URL: "http://proxy:3128"}
	if u, _ := f(req); u == nil || u.Host != "proxy:3128" {
		t.Fatalf("got %v, want proxy:3128", u)
	}
}

func TestFuncSystemFallsBackToEnvironment(t *testing.T) {
	useSystem(t, nil)
	// http.ProxyFromEnvironment caches the environment once per process, so only
	// check that the fallback path runs without error.
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if _, err := Func(func() Config { return Config{} })(req); err != nil {
		t.Fatal(err)
	}
}
