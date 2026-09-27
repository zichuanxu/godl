// Package settings defines the user-editable service settings (DESIGN.md
// sections 4, 4.2, and 4.3).
package settings

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zichuanxu/nimget/internal/netproxy"
)

const (
	MaxConnections     = 32
	MaxHostConnections = 64
)

type Settings struct {
	// MaxConcurrent is the number of downloads running at once.
	MaxConcurrent int `json:"maxConcurrent"`
	// Connections is the default per download, 1 to 32.
	Connections int `json:"connections"`
	// HostConnections caps connections to one host across all downloads.
	HostConnections int `json:"hostConnections"`
	// SpeedLimit caps all downloads together in bytes per second; 0 is none.
	SpeedLimit int64           `json:"speedLimit"`
	Schedule   Schedule        `json:"schedule"`
	Proxy      netproxy.Config `json:"proxy"`
	Sites      []Site          `json:"sites"`
	// ExtraRoots are directories picked in the GUI; downloads may be written
	// under them as well as under the service's download roots.
	ExtraRoots []string `json:"extraRoots"`
	Desktop    Desktop  `json:"desktop"`
}

// Desktop holds preferences only the desktop app reads.
type Desktop struct {
	ClipboardMonitor bool `json:"clipboardMonitor"`
	Notifications    bool `json:"notifications"`
	// AskDirectory asks for a folder on every add instead of using the
	// category folders.
	AskDirectory bool `json:"askDirectory"`
	// OpenWhenDone opens each file with its default app when it completes.
	OpenWhenDone bool `json:"openWhenDone"`
	// FFmpeg is the ffmpeg executable for converting streams to MP4; empty
	// means ffmpeg on PATH.
	FFmpeg string `json:"ffmpeg,omitempty"`
	// CheckUpdates polls GitHub Releases once a week for a newer version.
	CheckUpdates bool `json:"checkUpdates"`
	// Language is the interface language: "en", "zh-CN", or empty for the
	// system's.
	Language string `json:"language,omitempty"`
}

// Languages are the interface languages the desktop app ships.
var Languages = []string{"en", "zh-CN"}

// Schedule runs the queue only between Start and Stop ("HH:MM" local time,
// wrapping past midnight) and applies time-of-day speed limits. Empty Start and
// Stop run the queue all day.
type Schedule struct {
	Start      string      `json:"start,omitempty"`
	Stop       string      `json:"stop,omitempty"`
	SpeedRules []SpeedRule `json:"speedRules,omitempty"`
}

// SpeedRule replaces the global speed limit between From and To.
type SpeedRule struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Limit int64  `json:"limit"`
}

// Site overrides settings for a host and its subdomains. Zero fields inherit.
type Site struct {
	Host            string            `json:"host"`
	Connections     int               `json:"connections,omitempty"`
	HostConnections int               `json:"hostConnections,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Username        string            `json:"username,omitempty"`
	Password        string            `json:"password,omitempty"`
}

// Masked stands in for a stored site password in settings shown to clients;
// saving it back keeps the stored password.
const Masked = "********"

// Masked returns the settings with site passwords and the proxy password
// replaced by Masked.
func (s Settings) Masked() Settings {
	s.Sites = append([]Site(nil), s.Sites...)
	for i := range s.Sites {
		if s.Sites[i].Password != "" {
			s.Sites[i].Password = Masked
		}
	}
	if u, err := url.Parse(s.Proxy.URL); err == nil && u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), Masked)
			s.Proxy.URL = u.String()
		}
	}
	return s
}

// Unmask restores the passwords that a client sent back as Masked from
// stored, matching sites by host.
func (s Settings) Unmask(stored Settings) (Settings, error) {
	s.Sites = append([]Site(nil), s.Sites...)
	for i := range s.Sites {
		if s.Sites[i].Password != Masked {
			continue
		}
		s.Sites[i].Password = ""
		for _, old := range stored.Sites {
			if strings.EqualFold(old.Host, s.Sites[i].Host) {
				s.Sites[i].Password = old.Password
			}
		}
		if s.Sites[i].Password == "" {
			return s, fmt.Errorf("site %s: its password was renamed or removed; enter it again", s.Sites[i].Host)
		}
	}
	if u, err := url.Parse(s.Proxy.URL); err == nil && u.User != nil {
		if pass, _ := u.User.Password(); pass == Masked {
			old, err := url.Parse(stored.Proxy.URL)
			storedPass := ""
			if err == nil && old.User != nil {
				storedPass, _ = old.User.Password()
			}
			if storedPass == "" {
				return s, errors.New("proxy password was removed; enter it again")
			}
			u.User = url.UserPassword(u.User.Username(), storedPass)
			s.Proxy.URL = u.String()
		}
	}
	return s, nil
}

func Default() Settings {
	return Settings{
		MaxConcurrent: 3, Connections: 8, HostConnections: 16,
		Proxy:   netproxy.Config{Mode: netproxy.ModeSystem},
		Desktop: Desktop{ClipboardMonitor: true, Notifications: true, CheckUpdates: true},
	}
}

func (s Settings) Validate() error {
	var errs []error
	if s.MaxConcurrent < 1 || s.MaxConcurrent > 64 {
		errs = append(errs, fmt.Errorf("maxConcurrent must be in [1, 64], got %d", s.MaxConcurrent))
	}
	errs = append(errs, checkConnections("", s.Connections, s.HostConnections, false))
	if s.SpeedLimit < 0 {
		errs = append(errs, errors.New("speedLimit cannot be negative"))
	}
	if (s.Schedule.Start == "") != (s.Schedule.Stop == "") {
		errs = append(errs, errors.New("schedule start and stop must be set together"))
	}
	for _, t := range []string{s.Schedule.Start, s.Schedule.Stop} {
		if _, err := clock(t); t != "" && err != nil {
			errs = append(errs, err)
		}
	}
	for _, r := range s.Schedule.SpeedRules {
		_, errFrom := clock(r.From)
		_, errTo := clock(r.To)
		errs = append(errs, errFrom, errTo)
		if r.Limit < 0 {
			errs = append(errs, errors.New("speed rule limit cannot be negative"))
		}
	}
	errs = append(errs, s.Proxy.Validate())
	for _, site := range s.Sites {
		errs = append(errs, site.validate())
	}
	if s.Desktop.Language != "" && !slices.Contains(Languages, s.Desktop.Language) {
		errs = append(errs, fmt.Errorf("language must be one of %v or empty, got %q", Languages, s.Desktop.Language))
	}
	for _, dir := range s.ExtraRoots {
		if !filepath.IsAbs(dir) {
			errs = append(errs, fmt.Errorf("extra root %q must be an absolute path", dir))
		}
	}
	return errors.Join(errs...)
}

func (s Site) validate() error {
	host := strings.ToLower(strings.TrimSpace(s.Host))
	if host == "" || strings.ContainsAny(host, "/:@ ") {
		return fmt.Errorf("site host %q must be a bare host name", s.Host)
	}
	errs := []error{checkConnections(host+": ", s.Connections, s.HostConnections, true)}
	for name, value := range s.Headers {
		errs = append(errs, ValidateHeader(name, value))
	}
	return errors.Join(errs...)
}

func checkConnections(prefix string, conns, hostConns int, optional bool) error {
	low := 1
	if optional {
		low = 0 // unset: inherit
	}
	if conns < low || conns > MaxConnections {
		return fmt.Errorf("%sconnections must be in [1, %d], got %d", prefix, MaxConnections, conns)
	}
	if hostConns < low || hostConns > MaxHostConnections {
		return fmt.Errorf("%shostConnections must be in [1, %d], got %d", prefix, MaxHostConnections, hostConns)
	}
	return nil
}

// ValidateHeader rejects header names and values that cannot be sent verbatim.
func ValidateHeader(name, value string) error {
	if name == "" || strings.ContainsAny(name, " :\r\n\t") || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("invalid header %q", name)
	}
	return nil
}

// Site returns the most specific site entry matching host.
func (s Settings) Site(host string) Site {
	host = strings.ToLower(host)
	var best Site
	for _, site := range s.Sites {
		h := strings.ToLower(strings.TrimSpace(site.Host))
		if (host == h || strings.HasSuffix(host, "."+h)) && len(h) > len(best.Host) {
			best = site
			best.Host = h
		}
	}
	return best
}

// ConnectionsFor resolves the connection count for a download from host; an
// explicit per-download value wins, then the site's, then the default.
func (s Settings) ConnectionsFor(host string, requested int) int {
	if requested > 0 {
		return min(requested, MaxConnections)
	}
	if c := s.Site(host).Connections; c > 0 {
		return c
	}
	return s.Connections
}

func (s Settings) HostConnectionsFor(host string) int {
	if c := s.Site(host).HostConnections; c > 0 {
		return c
	}
	return s.HostConnections
}

// HeadersFor returns the site's extra headers and credentials.
func (s Settings) HeadersFor(host string) http.Header {
	site := s.Site(host)
	req := http.Request{Header: make(http.Header)}
	for name, value := range site.Headers {
		req.Header.Set(name, value)
	}
	if site.Username != "" || site.Password != "" {
		req.SetBasicAuth(site.Username, site.Password)
	}
	return req.Header
}

// QueueOpen reports whether the schedule lets the queue run at now.
func (s Settings) QueueOpen(now time.Time) bool {
	return s.Schedule.Start == "" || within(now, s.Schedule.Start, s.Schedule.Stop)
}

// SpeedLimitAt returns the global limit in force at now: the first matching
// speed rule, else SpeedLimit. 0 means unlimited.
func (s Settings) SpeedLimitAt(now time.Time) int64 {
	for _, r := range s.Schedule.SpeedRules {
		if within(now, r.From, r.To) {
			return r.Limit
		}
	}
	return s.SpeedLimit
}

// within reports whether now's local time of day is in [from, to), wrapping
// past midnight. Equal bounds cover the whole day.
func within(now time.Time, from, to string) bool {
	f, errF := clock(from)
	t, errT := clock(to)
	if errF != nil || errT != nil {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	switch {
	case f == t:
		return true
	case f < t:
		return m >= f && m < t
	default:
		return m >= f || m < t
	}
}

// clock parses "HH:MM" into minutes after midnight.
func clock(value string) (int, error) {
	t, err := time.Parse("15:04", value)
	if err != nil {
		return 0, fmt.Errorf("invalid time of day %q, want HH:MM", value)
	}
	return t.Hour()*60 + t.Minute(), nil
}
