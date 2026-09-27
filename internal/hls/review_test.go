package hls

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// Credentials for the playlist's host never go to a segment or key host that
// the playlist names.
func TestHeadersStayWithThePlaylistHost(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]http.Header{}
	record := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[name] = r.Header.Clone()
			mu.Unlock()
			_, _ = w.Write([]byte("segment"))
		}
	}
	other := httptest.NewServer(record("other"))
	defer other.Close()
	foreign := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	s := newServer(t)
	s.set("/media.m3u8", []byte(fmt.Sprintf("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nlocal.ts\n#EXTINF:4,\n%s/x.ts\n#EXT-X-ENDLIST\n", foreign)))
	s.set("/local.ts", []byte("local"))
	opts := fastOpts()
	opts.Headers = http.Header{"Cookie": {"session=1"}, "Authorization": {"Bearer t"}, "X-Api-Key": {"k"}, "Referer": {"https://page.test/"}}
	var local http.Header
	s.setHook(func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if r.URL.Path == "/local.ts" {
			local = r.Header.Clone()
		}
		return false
	})
	if err := Download(context.Background(), s.URL+"/media.m3u8", destPath(t), opts, nil); err != nil {
		t.Fatal(err)
	}
	if local.Get("Cookie") != "session=1" || local.Get("X-Api-Key") != "k" {
		t.Fatalf("playlist host lost its headers: %v", local)
	}
	got := seen["other"]
	for _, h := range []string{"Cookie", "Authorization", "X-Api-Key"} {
		if got.Get(h) != "" {
			t.Errorf("%s sent to another host", h)
		}
	}
	if got.Get("Referer") == "" {
		t.Error("portable Referer dropped")
	}
}

// Rotated query tokens do not discard finished segments, and the fresh
// URLs are used for the rest.
func TestResumeSurvivesRotatedTokens(t *testing.T) {
	s := newServer(t)
	playlist := func(token string) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
		for i := range 4 {
			fmt.Fprintf(&b, "#EXTINF:4,\ns%d.ts?token=%s\n", i, token)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		s.set("/media.m3u8", []byte(b.String()))
	}
	var want []byte
	for i := range 4 {
		seg := payload(i, 2000)
		s.set(fmt.Sprintf("/s%d.ts", i), seg)
		want = append(want, seg...)
	}
	token := "old"
	var mu sync.Mutex
	s.setHook(func(w http.ResponseWriter, r *http.Request, _ int) bool {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".ts") && r.URL.Query().Get("token") != token {
			http.Error(w, "expired", http.StatusForbidden)
			return true
		}
		return false
	})
	playlist("old")
	s.set("/s2.ts", nil) // fails the first run after s0 and s1
	dest := destPath(t)
	opts := fastOpts()
	opts.Concurrency = 1
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, opts, nil); err == nil {
		t.Fatal("first run succeeded")
	}
	s.set("/s2.ts", want[4000:6000])
	mu.Lock()
	token = "new"
	mu.Unlock()
	playlist("new")
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, opts, nil); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, want)
	if n := s.count("/s0.ts"); n != 1 {
		t.Fatalf("s0 fetched %d times; finished segments were discarded", n)
	}
}

// A directory at dest.hls that nimget did not create is never deleted.
func TestForeignWorkDirIsKept(t *testing.T) {
	s := newServer(t)
	clearStream(s, 2)
	dest := destPath(t)
	if err := os.MkdirAll(dest+".hls", 0o755); err != nil {
		t.Fatal(err)
	}
	mine := dest + ".hls/notes.txt"
	if err := os.WriteFile(mine, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), s.URL+"/media.m3u8", dest, fastOpts(), nil); err == nil {
		t.Fatal("download reused a foreign directory")
	}
	if err := Discard(dest); err == nil {
		t.Fatal("Discard accepted a foreign directory")
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatalf("user file deleted: %v", err)
	}
}

// A 206 for a different range than requested is rejected.
func TestWrongContentRangeRejected(t *testing.T) {
	s := newServer(t)
	s.set("/media.m3u8", []byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-BYTERANGE:10@100\n#EXTINF:4,\nall.ts\n#EXT-X-ENDLIST\n"))
	s.setHook(func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if r.URL.Path != "/all.ts" {
			return false
		}
		w.Header().Set("Content-Range", "bytes 0-9/1000")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("0123456789"))
		return true
	})
	if err := Download(context.Background(), s.URL+"/media.m3u8", destPath(t), fastOpts(), nil); err == nil || !strings.Contains(err.Error(), "Content-Range") {
		t.Fatalf("err = %v", err)
	}
}
