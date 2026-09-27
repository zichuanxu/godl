package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderManifests(t *testing.T) {
	dir := t.TempDir()
	var sums strings.Builder
	for i, name := range []string{
		"nimget_1.2.3_darwin_arm64.tar.gz", "nimget_1.2.3_darwin_amd64.tar.gz", "nimget_1.2.3_linux_arm64.tar.gz",
		"nimget_1.2.3_linux_amd64.tar.gz", "nimget_1.2.3_windows_amd64.zip", "nimget-desktop_1.2.3_macos_universal.dmg",
		"nimget-desktop_1.2.3_windows_amd64.zip", "nimget-desktop_1.2.3_windows_amd64_setup.exe",
	} {
		fmt.Fprintf(&sums, "%064x  %s\n", i+1, name)
	}
	path := filepath.Join(dir, "sums.txt")
	if err := os.WriteFile(path, []byte(sums.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := run("1.2.3", []string{path}, out); err != nil {
		t.Fatal(err)
	}
	cask, _ := os.ReadFile(filepath.Join(out, "homebrew-tap/Casks/nimget-desktop.rb"))
	if !strings.Contains(string(cask), fmt.Sprintf(`sha256 "%064x"`, 6)) || !strings.Contains(string(cask), `version "1.2.3"`) || !strings.Contains(string(cask), `app "NimGet.app"`) {
		t.Fatalf("cask:\n%s", cask)
	}
	scoop, _ := os.ReadFile(filepath.Join(out, "scoop-bucket/bucket/nimget-desktop.json"))
	if !strings.Contains(string(scoop), `"shortcuts": [["nimget.exe", "NimGet"]]`) {
		t.Fatalf("scoop desktop:\n%s", scoop)
	}
	formula, _ := os.ReadFile(filepath.Join(out, "homebrew-tap/Formula/nimget.rb"))
	if strings.Count(string(formula), "sha256") != 4 {
		t.Fatalf("formula:\n%s", formula)
	}
	if err := run("1.2.4", []string{path}, out); err == nil {
		t.Fatal("missing checksums accepted")
	}
	if err := run("v1.2.3", []string{path}, out); err == nil {
		t.Fatal("v-prefixed version accepted")
	}
}
