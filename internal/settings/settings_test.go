package settings

import (
	"strings"
	"testing"
	"time"
)

func at(hhmm string) time.Time {
	t, _ := time.ParseInLocation("15:04", hhmm, time.Local)
	return time.Date(2026, 9, 27, t.Hour(), t.Minute(), 0, 0, time.Local)
}

func TestScheduleWindowWrapsMidnight(t *testing.T) {
	s := Default()
	s.Schedule = Schedule{Start: "23:00", Stop: "06:30"}
	for hhmm, open := range map[string]bool{"22:59": false, "23:00": true, "03:00": true, "06:29": true, "06:30": false, "12:00": false} {
		if got := s.QueueOpen(at(hhmm)); got != open {
			t.Errorf("QueueOpen(%s) = %v, want %v", hhmm, got, open)
		}
	}
	if !Default().QueueOpen(at("12:00")) {
		t.Fatal("an empty schedule must keep the queue open")
	}
}

func TestSpeedRules(t *testing.T) {
	s := Default()
	s.SpeedLimit = 100
	s.Schedule.SpeedRules = []SpeedRule{{From: "09:00", To: "17:00", Limit: 10}, {From: "08:00", To: "18:00", Limit: 50}}
	cases := map[string]int64{"07:59": 100, "08:30": 50, "09:00": 10, "16:59": 10, "17:30": 50, "18:00": 100}
	for hhmm, want := range cases {
		if got := s.SpeedLimitAt(at(hhmm)); got != want {
			t.Errorf("SpeedLimitAt(%s) = %d, want %d", hhmm, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default settings invalid: %v", err)
	}
	bad := []func(*Settings){
		func(s *Settings) { s.MaxConcurrent = 0 },
		func(s *Settings) { s.Connections = 33 },
		func(s *Settings) { s.HostConnections = 0 },
		func(s *Settings) { s.SpeedLimit = -1 },
		func(s *Settings) { s.Schedule.Start = "01:00" },
		func(s *Settings) { s.Schedule = Schedule{Start: "25:00", Stop: "01:00"} },
		func(s *Settings) { s.Schedule.SpeedRules = []SpeedRule{{From: "x", To: "01:00"}} },
		func(s *Settings) { s.Proxy.Mode = "bogus" },
		func(s *Settings) { s.Sites = []Site{{Host: "http://example.com"}} },
		func(s *Settings) { s.Sites = []Site{{Host: "example.com", Connections: 99}} },
		func(s *Settings) { s.Sites = []Site{{Host: "example.com", Headers: map[string]string{"X": "a\r\nb"}}} },
		func(s *Settings) { s.Desktop.Language = "fr" },
	}
	for i, mutate := range bad {
		s := Default()
		mutate(&s)
		if s.Validate() == nil {
			t.Errorf("case %d: invalid settings accepted: %+v", i, s)
		}
	}
}

func TestSiteOverrides(t *testing.T) {
	s := Default()
	s.Sites = []Site{
		{Host: "example.com", Connections: 4, Headers: map[string]string{"X-Site": "outer"}},
		{Host: "cdn.example.com", HostConnections: 2, Username: "u", Password: "p"},
	}
	if got := s.ConnectionsFor("a.example.com", 0); got != 4 {
		t.Fatalf("subdomain connections = %d, want 4", got)
	}
	if got := s.ConnectionsFor("cdn.example.com", 0); got != 8 {
		t.Fatalf("most specific site inherits connections: got %d, want 8", got)
	}
	if got := s.ConnectionsFor("example.com", 12); got != 12 {
		t.Fatalf("explicit connections = %d, want 12", got)
	}
	if got := s.HostConnectionsFor("cdn.example.com"); got != 2 {
		t.Fatalf("host connections = %d, want 2", got)
	}
	if got := s.HostConnectionsFor("notexample.com"); got != 16 {
		t.Fatalf("unrelated host matched a site: %d", got)
	}
	if h := s.HeadersFor("cdn.example.com"); h.Get("Authorization") != "Basic dTpw" {
		t.Fatalf("credentials header = %q", h.Get("Authorization"))
	}
	if h := s.HeadersFor("www.example.com"); h.Get("X-Site") != "outer" {
		t.Fatalf("site header missing: %v", h)
	}
}

func TestMaskedPasswordsRoundTrip(t *testing.T) {
	stored := Default()
	stored.Sites = []Site{{Host: "a.test", Password: "secret"}, {Host: "b.test"}}
	shown := stored.Masked()
	if shown.Sites[0].Password != Masked || shown.Sites[1].Password != "" || stored.Sites[0].Password != "secret" {
		t.Fatalf("masked = %+v (stored %+v)", shown.Sites, stored.Sites)
	}
	back, err := shown.Unmask(stored)
	if err != nil || back.Sites[0].Password != "secret" {
		t.Fatalf("unmasked = %+v, %v", back.Sites, err)
	}
	shown.Sites = append(shown.Sites, Site{Host: "c.test", Password: Masked})
	if _, err := shown.Unmask(stored); err == nil {
		t.Fatal("a masked password without a stored one was accepted")
	}

	stored.Proxy.URL = "socks5://me:pw@proxy.test:1080"
	shown = stored.Masked()
	if strings.Contains(shown.Proxy.URL, "pw") || !strings.Contains(shown.Proxy.URL, "me:") {
		t.Fatalf("proxy URL not masked: %s", shown.Proxy.URL)
	}
	shown.Sites = shown.Sites[:2]
	if back, err := shown.Unmask(stored); err != nil || back.Proxy.URL != stored.Proxy.URL {
		t.Fatalf("proxy unmask = %q, %v", back.Proxy.URL, err)
	}
}
