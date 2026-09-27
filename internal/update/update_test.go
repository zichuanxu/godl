package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"v1.0.1", "v1.0.0", true},
		{"v1.10.0", "v1.9.9", true},
		{"v1.0.0", "v1.0.0", false},
		{"v0.9.0", "v1.0.0", false},
		{"v1.0.0", "v1.0.0-rc.1", true},
		{"v1.0.0-rc.2", "v1.0.0-rc.1", true},
		{"v1.0.0-rc.1", "v1.0.0", false},
		{"v2.0.0", "dev", false},
		{"garbage", "v1.0.0", false},
		{"1.2.3+meta", "1.2.2", true},
	}
	for _, c := range cases {
		if got := Newer(c.candidate, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.candidate, c.current, got)
		}
	}
}

func TestCheckPollsWeekly(t *testing.T) {
	var hits atomic.Int64
	tag := "v1.2.0"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://github.com/zichuanxu/nimget/releases/tag/` + tag + `"}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "update.json")
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	c := &Checker{Client: server.Client(), URL: server.URL, Path: path}
	check := func(current string, at time.Time) (Release, bool, error) {
		return c.Check(context.Background(), current, at)
	}

	rel, ok, err := check("v1.1.0", now)
	if err != nil || !ok || rel.Version != "v1.2.0" {
		t.Fatalf("first check = %+v %v %v", rel, ok, err)
	}
	tag = "v1.3.0"
	if rel, _, _ := check("v1.1.0", now.Add(24*time.Hour)); rel.Version != "v1.2.0" || hits.Load() != 1 {
		t.Fatalf("polled again within a week: %+v, %d hits", rel, hits.Load())
	}
	if rel, _, _ := check("v1.1.0", now.Add(Interval)); rel.Version != "v1.3.0" || hits.Load() != 2 {
		t.Fatalf("weekly check = %+v, %d hits", rel, hits.Load())
	}
	if _, ok, _ := check("v1.3.0", now.Add(Interval)); ok {
		t.Fatal("current version reported as an update")
	}
	if _, _, err := check("dev", now); err == nil {
		t.Fatal("dev build checked for updates")
	}
}

func TestLatestRejectsForeignLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.0.0","html_url":"https://evil.test/"}`))
	}))
	defer server.Close()
	rel, err := Latest(context.Background(), server.Client(), server.URL)
	if err != nil || rel.URL != "https://github.com/zichuanxu/nimget/releases" {
		t.Fatalf("rel = %+v, %v", rel, err)
	}
}

// With an unwritable state file the in-memory copy still keeps the check
// weekly, and the release is still reported.
func TestCheckWithUnwritableState(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0","html_url":"https://github.com/zichuanxu/nimget/releases/tag/v2.0.0"}`))
	}))
	defer server.Close()
	c := &Checker{Client: server.Client(), URL: server.URL, Path: filepath.Join(t.TempDir(), "missing-dir", "update.json")}
	now := time.Now()
	for i := range 3 {
		rel, ok, err := c.Check(context.Background(), "v1.0.0", now.Add(time.Duration(i)*time.Hour))
		if err != nil || !ok || rel.Version != "v2.0.0" {
			t.Fatalf("check %d = %+v %v %v", i, rel, ok, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("polled %d times within a week", hits.Load())
	}
}
