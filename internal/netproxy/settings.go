package netproxy

import (
	"net"
	"net/netip"
	"net/url"
	"path"
	"strings"
)

// settings is an OS proxy configuration normalized across platforms.
type settings struct {
	http, https, socks *url.URL
	bypass             []string // lowercased hosts, wildcards, or CIDRs
	bypassSimple       bool     // bypass dotless hostnames (<local> / ExcludeSimpleHostnames)
}

// proxyFor picks the proxy for a request URL: HTTPS proxy for https, HTTP proxy
// for http, otherwise SOCKS. Nil means connect directly.
func (s *settings) proxyFor(u *url.URL) *url.URL {
	if s.bypassed(strings.ToLower(u.Hostname())) {
		return nil
	}
	switch {
	case u.Scheme == "https" && s.https != nil:
		return s.https
	case u.Scheme == "http" && s.http != nil:
		return s.http
	}
	return s.socks
}

// bypassed reports whether host (lowercase, no port) matches a bypass rule.
func (s *settings) bypassed(host string) bool {
	if s.bypassSimple && !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return true
	}
	ip, ipErr := netip.ParseAddr(host)
	for _, r := range s.bypass {
		switch {
		case r == host:
			return true
		case strings.Contains(r, "/"):
			if p, err := netip.ParsePrefix(expandCIDR(r)); err == nil && ipErr == nil && p.Contains(ip.Unmap()) {
				return true
			}
		case strings.HasPrefix(r, "."):
			if strings.HasSuffix(host, r) {
				return true
			}
		case strings.ContainsAny(r, "*?"):
			if ok, _ := path.Match(r, host); ok {
				return true
			}
		}
	}
	return false
}

// expandCIDR turns macOS shorthand like "169.254/16" into "169.254.0.0/16".
func expandCIDR(r string) string {
	addr, bits, _ := strings.Cut(r, "/")
	if strings.Contains(addr, ":") {
		return r
	}
	for strings.Count(addr, ".") < 3 {
		addr += ".0"
	}
	return addr + "/" + bits
}

// proxyURL builds scheme://host[:port], or nil when host is empty.
func proxyURL(scheme, host, port string) *url.URL {
	if host == "" {
		return nil
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	}
	return &url.URL{Scheme: scheme, Host: host}
}

// empty reports whether no proxy is configured, so the caller falls back to the environment.
func (s *settings) empty() bool {
	return s.http == nil && s.https == nil && s.socks == nil
}

// parseScutil parses `scutil --proxy` output. Only top-level keys are used, so the
// per-interface __SCOPED__ dictionaries are ignored. It returns nil when no
// HTTP, HTTPS, or SOCKS proxy is enabled.
func parseScutil(out string) *settings {
	kv := map[string]string{}
	var exceptions []string
	depth, inExceptions := 0, false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "}" {
			depth--
			if depth <= 1 {
				inExceptions = false
			}
			continue
		}
		key, val, ok := strings.Cut(line, " : ")
		if strings.HasSuffix(line, "{") {
			if depth == 1 && key == "ExceptionsList" {
				inExceptions = true
			}
			depth++
			continue
		}
		switch {
		case !ok:
		case depth == 1:
			kv[key] = val
		case depth == 2 && inExceptions:
			exceptions = append(exceptions, strings.ToLower(val))
		}
	}

	get := func(prefix, scheme string) *url.URL {
		if kv[prefix+"Enable"] != "1" {
			return nil
		}
		return proxyURL(scheme, kv[prefix+"Proxy"], kv[prefix+"Port"])
	}
	s := &settings{
		http:         get("HTTP", "http"),
		https:        get("HTTPS", "http"),
		socks:        get("SOCKS", "socks5"),
		bypass:       exceptions,
		bypassSimple: kv["ExcludeSimpleHostnames"] == "1",
	}
	if s.empty() {
		return nil
	}
	return s
}

// parseWindows parses the WinINet ProxyServer and ProxyOverride registry values.
// ProxyServer is either "host:port" (HTTP and HTTPS) or "http=h:p;https=h:p;socks=h:p".
// It returns nil when no usable proxy is configured.
func parseWindows(server, override string) *settings {
	s := &settings{}
	for _, entry := range strings.Split(server, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		proto, addr, ok := strings.Cut(entry, "=")
		if !ok {
			u := winProxyURL("http", entry)
			s.http, s.https = u, u
			continue
		}
		switch strings.ToLower(proto) {
		case "http":
			s.http = winProxyURL("http", addr)
		case "https":
			s.https = winProxyURL("http", addr)
		case "socks":
			// ponytail: Windows "socks=" means SOCKS4, but Go's transport only speaks
			// SOCKS5; most SOCKS servers accept both.
			s.socks = winProxyURL("socks5", addr)
		}
	}
	for _, r := range strings.Split(override, ";") {
		r = strings.ToLower(strings.TrimSpace(r))
		switch r {
		case "":
		case "<local>":
			s.bypassSimple = true
		default:
			s.bypass = append(s.bypass, r)
		}
	}
	if s.empty() {
		return nil
	}
	return s
}

// winProxyURL parses "host:port" or "scheme://host:port".
func winProxyURL(scheme, addr string) *url.URL {
	addr = strings.TrimSpace(addr)
	if strings.Contains(addr, "://") {
		if u, err := url.Parse(addr); err == nil && u.Host != "" {
			return u
		}
		return nil
	}
	if addr == "" {
		return nil
	}
	return &url.URL{Scheme: scheme, Host: addr}
}
