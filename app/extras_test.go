package main

import (
	"testing"

	"github.com/zichuanxu/godl/internal/download"
)

func TestDrained(t *testing.T) {
	items := []download.Item{{Status: download.StatusCompleted}, {Status: download.StatusPaused}, {Status: download.StatusFailed}}
	if !drained(items) {
		t.Fatal("completed, paused, and failed downloads block the queue")
	}
	for _, s := range []download.Status{download.StatusQueued, download.StatusRunning} {
		if drained(append(items, download.Item{Status: s})) {
			t.Fatalf("a %s download counted as done", s)
		}
	}
}

func TestWhenDoneFiresOnce(t *testing.T) {
	d := &Desktop{}
	if err := d.SetWhenDone("reboot"); err == nil {
		t.Fatal("unknown action accepted")
	}
	if err := d.SetWhenDone("sleep"); err != nil || d.WhenDone() != "sleep" {
		t.Fatalf("SetWhenDone: %v %q", err, d.WhenDone())
	}
	_ = d.SetWhenDone("")
	if err := d.PerformWhenDone("sleep"); err == nil {
		t.Fatal("a cancelled action ran")
	}
}

func TestOfferable(t *testing.T) {
	for text, want := range map[string]bool{
		"https://example.com/a.zip":   true,
		"  http://x.test/a  ":         true,
		"curl 'https://x.test/a'":     true,
		"ftp://x.test/a":              false,
		"see https://x.test/a":        false,
		"https://x.test/a\nhttps://b": false,
	} {
		if got := offerable(text); got != want {
			t.Errorf("offerable(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestOnlyPassiveFilesOpenThemselves(t *testing.T) {
	for path, want := range map[string]bool{"/d/a.MP4": true, "/d/b.pdf": true, "/d/c.command": false, "/d/d.exe": false, "/d/e.app": false, "/d/noext": false} {
		if got := passive(path); got != want {
			t.Errorf("passive(%q) = %v", path, got)
		}
	}
}
