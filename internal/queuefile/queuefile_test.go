package queuefile

import (
	"strings"
	"testing"

	"github.com/zichuanxu/godl/internal/download"
)

func TestExportImportRoundTrip(t *testing.T) {
	items := []download.Item{
		{URL: "https://x/a", Destination: "/d/a", Status: download.StatusQueued, Priority: 1, SpeedLimit: 5, Headers: map[string]string{"Cookie": "s=1"}},
		{URL: "https://x/b", Destination: "/d/b", Status: download.StatusCompleted},
		{URL: "https://x/c", Destination: "/d/c", Status: download.StatusFailed, Checksum: "sha256:00"},
	}
	data, err := Export(items, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "s=1") {
		t.Fatal("headers exported without withHeaders")
	}
	reqs, err := Import(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[0].URL != "https://x/a" || reqs[0].Priority != 1 || reqs[0].SpeedLimit != 5 || reqs[1].Checksum != "sha256:00" {
		t.Fatalf("imported %+v", reqs)
	}
	data, _ = Export(items, true)
	if reqs, _ := Import(data); reqs[0].Headers["Cookie"] != "s=1" {
		t.Fatal("headers lost with withHeaders")
	}
}

func TestImportURLList(t *testing.T) {
	reqs, err := Import([]byte("# my list\nhttps://x/1\n\n  https://x/2  \r\n"))
	if err != nil || len(reqs) != 2 || reqs[1].URL != "https://x/2" {
		t.Fatalf("got %+v, %v", reqs, err)
	}
	for _, bad := range []string{"", "# only comments", `{"version":9,"downloads":[]}`, `{"version":1,"extra":1}`} {
		if _, err := Import([]byte(bad)); err == nil {
			t.Errorf("Import(%q) accepted", bad)
		}
	}
}
