package quarantine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.exe")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Mark(path, "https://user:pw@example.com/dl/setup.exe?token=secret"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		data, err := os.ReadFile(path + ":Zone.Identifier")
		if err != nil || !strings.Contains(string(data), "ZoneId=3") || strings.Contains(string(data), "secret") || strings.Contains(string(data), "pw") {
			t.Fatalf("Zone.Identifier = %q, %v", data, err)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != "x" {
		t.Fatal("marking changed the content")
	}
	if got := origin("https://u:p@h.test/a?b=c#d"); got != "https://h.test/a" {
		t.Fatalf("origin = %q", got)
	}
}
