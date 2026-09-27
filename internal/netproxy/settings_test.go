package netproxy

import (
	"net/url"
	"testing"
)

const scutilFixture = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : 10.0.0.0/8
    3 : Intranet.Example.com
    4 : .corp.test
  }
  ExcludeSimpleHostnames : 1
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : proxy.example.com
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : secure.example.com
  ProxyAutoConfigEnable : 0
  SOCKSEnable : 0
  SOCKSPort : 1080
  SOCKSProxy : socks.example.com
  __SCOPED__ : <dictionary> {
    en0 : <dictionary> {
      ExceptionsList : <array> {
        0 : scoped.example.com
      }
      HTTPEnable : 0
      SOCKSEnable : 1
    }
  }
}
`

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func proxyForString(t *testing.T, s *settings, target string) string {
	t.Helper()
	if u := s.proxyFor(mustURL(t, target)); u != nil {
		return u.String()
	}
	return ""
}

func TestParseScutil(t *testing.T) {
	s := parseScutil(scutilFixture)
	if s == nil {
		t.Fatal("parseScutil returned nil")
	}
	if s.socks != nil {
		t.Errorf("socks = %v, want nil (scoped SOCKSEnable must be ignored)", s.socks)
	}
	if !s.bypassSimple {
		t.Error("bypassSimple = false, want true")
	}
	if len(s.bypass) != 5 {
		t.Errorf("bypass = %q, want 5 top-level exceptions", s.bypass)
	}
	tests := map[string]string{
		"http://example.com/":             "http://proxy.example.com:8080",
		"https://example.com/":            "http://secure.example.com:8443",
		"https://printer.local/":          "",
		"http://169.254.10.20/":           "",
		"http://10.1.2.3:8080/":           "",
		"http://11.1.2.3/":                "http://proxy.example.com:8080",
		"https://intranet.example.com/":   "",
		"https://a.b.corp.test/":          "",
		"https://corp.test/":              "http://secure.example.com:8443",
		"http://nas/":                     "",
		"http://scoped.example.com/":      "http://proxy.example.com:8080",
		"http://INTRANET.EXAMPLE.COM:81/": "",
	}
	for target, want := range tests {
		if got := proxyForString(t, s, target); got != want {
			t.Errorf("%s: got %q, want %q", target, got, want)
		}
	}
}

func TestParseScutilDisabled(t *testing.T) {
	out := "<dictionary> {\n  FTPPassive : 1\n  HTTPEnable : 0\n  HTTPSEnable : 0\n  SOCKSEnable : 0\n}\n"
	if s := parseScutil(out); s != nil {
		t.Errorf("got %+v, want nil", s)
	}
}

func TestParseScutilSOCKSOnly(t *testing.T) {
	out := "<dictionary> {\n  SOCKSEnable : 1\n  SOCKSPort : 1080\n  SOCKSProxy : 127.0.0.2\n}\n"
	s := parseScutil(out)
	for _, target := range []string{"http://example.com/", "https://example.com/"} {
		if got := proxyForString(t, s, target); got != "socks5://127.0.0.2:1080" {
			t.Errorf("%s: got %q", target, got)
		}
	}
}

func TestParseWindowsSingleServer(t *testing.T) {
	s := parseWindows("proxy.corp:3128", "")
	for target, want := range map[string]string{
		"http://example.com/":  "http://proxy.corp:3128",
		"https://example.com/": "http://proxy.corp:3128",
		"ftp://example.com/":   "",
	} {
		if got := proxyForString(t, s, target); got != want {
			t.Errorf("%s: got %q, want %q", target, got, want)
		}
	}
}

func TestParseWindowsPerProtocol(t *testing.T) {
	s := parseWindows("http=h1:80;https=h2:443;ftp=h3:21;socks=h4:1080", "")
	for target, want := range map[string]string{
		"http://example.com/":  "http://h1:80",
		"https://example.com/": "http://h2:443",
		"ftp://example.com/":   "socks5://h4:1080",
	} {
		if got := proxyForString(t, s, target); got != want {
			t.Errorf("%s: got %q, want %q", target, got, want)
		}
	}

	s = parseWindows("socks=h4:1080", "")
	if got := proxyForString(t, s, "https://example.com/"); got != "socks5://h4:1080" {
		t.Errorf("socks only: got %q", got)
	}
	s = parseWindows("https=https://h2:443", "")
	if got := proxyForString(t, s, "https://example.com/"); got != "https://h2:443" {
		t.Errorf("explicit scheme: got %q", got)
	}
	if s := parseWindows("", "<local>"); s != nil {
		t.Errorf("empty server: got %+v, want nil", s)
	}
}

func TestParseWindowsOverride(t *testing.T) {
	s := parseWindows("proxy:8080", " <local> ; *.Example.com;192.168.*;exact.host;; ")
	for target, want := range map[string]string{
		"http://intranet/":          "",
		"http://a.example.com/":     "",
		"http://a.b.example.com/":   "",
		"http://example.com/":       "http://proxy:8080",
		"http://192.168.1.10/":      "",
		"http://192.169.1.10/":      "http://proxy:8080",
		"http://exact.host/":        "",
		"http://sub.exact.host/":    "http://proxy:8080",
		"http://[2001:db8::1]:8080": "http://proxy:8080",
	} {
		if got := proxyForString(t, s, target); got != want {
			t.Errorf("%s: got %q, want %q", target, got, want)
		}
	}
}

func TestCIDRBypass(t *testing.T) {
	s := &settings{
		http:   proxyURL("http", "p", "1"),
		bypass: []string{"169.254/16", "172.16.0.0/12", "fd00::/8", "10/8", "bad/cidr"},
	}
	for host, want := range map[string]bool{
		"169.254.1.1": true,
		"169.255.1.1": false,
		"172.31.0.1":  true,
		"172.32.0.1":  false,
		"fd12::1":     true,
		"fe80::1":     false,
		"10.9.9.9":    true,
		"example.com": false,
	} {
		if got := s.bypassed(host); got != want {
			t.Errorf("bypassed(%q) = %v, want %v", host, got, want)
		}
	}
}
