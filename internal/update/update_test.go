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
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://github.com/zichuanxu/godl/releases/tag/` + tag + `"}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "update.json")
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)

	rel, ok, err := Check(context.Background(), server.Client(), server.URL, path, "v1.1.0", now)
	if err != nil || !ok || rel.Version != "v1.2.0" {
		t.Fatalf("first check = %+v %v %v", rel, ok, err)
	}
	tag = "v1.3.0"
	if rel, _, _ := Check(context.Background(), server.Client(), server.URL, path, "v1.1.0", now.Add(24*time.Hour)); rel.Version != "v1.2.0" || hits.Load() != 1 {
		t.Fatalf("polled again within a week: %+v, %d hits", rel, hits.Load())
	}
	if rel, _, _ := Check(context.Background(), server.Client(), server.URL, path, "v1.1.0", now.Add(Interval)); rel.Version != "v1.3.0" || hits.Load() != 2 {
		t.Fatalf("weekly check = %+v, %d hits", rel, hits.Load())
	}
	if _, ok, _ := Check(context.Background(), server.Client(), server.URL, path, "v1.3.0", now.Add(Interval)); ok {
		t.Fatal("current version reported as an update")
	}
	if _, _, err := Check(context.Background(), server.Client(), server.URL, path, "dev", now); err == nil {
		t.Fatal("dev build checked for updates")
	}
}

func TestLatestRejectsForeignLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.0.0","html_url":"https://evil.test/"}`))
	}))
	defer server.Close()
	rel, err := Latest(context.Background(), server.Client(), server.URL)
	if err != nil || rel.URL != "https://github.com/zichuanxu/godl/releases" {
		t.Fatalf("rel = %+v, %v", rel, err)
	}
}
