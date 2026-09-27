package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFileNameResolutionOrder(t *testing.T) {
	cases := []struct{ disposition, url, want string }{
		{"report.pdf", "https://example.com/x", "report.pdf"},
		{"", "https://example.com/dir/file%20name.zip?sig=1", "file name.zip"},
		{"", "https://example.com/", "example.com"},
		{"", "https://example.com/dir/", "dir"},
		{"../../etc/passwd", "https://example.com/a", "_.._etc_passwd"},
		{"..", "https://example.com/a.bin", "a.bin"},
	}
	for _, c := range cases {
		if got := fileName(c.disposition, c.url); got != c.want {
			t.Errorf("fileName(%q, %q) = %q, want %q", c.disposition, c.url, got, c.want)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"a/b\\c.txt":        "a_b_c.txt",
		"CON":               "_CON",
		"con.tar.gz":        "_con.tar.gz",
		"LPT9.txt":          "_LPT9.txt",
		"COM0.txt":          "_COM0.txt",
		"name. . ":          "name",
		".hidden":           "hidden",
		"a<b>c:d\"e|f?g*h":  "a_b_c_d_e_f_g_h",
		"tab\there\x00.bin": "tab_here_.bin",
		"café.txt":         "café.txt", // NFD -> NFC
		"   ":               "",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("字", 100) + ".mp4" // 300 bytes + ext
	got := sanitizeFilename(long)
	if len(got) > maxNameBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("long name = %q (%d bytes)", got, len(got))
	}
}

func TestFreeNameSkipsFilesPartsAndQueuedItems(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"clip.mp4", "clip (1).mp4.part"} {
		if err := os.WriteFile(filepath.Join(dir, p), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	queued := filepath.Join(dir, "clip (2).mp4")
	got, err := freeName(dir, "clip.mp4", func(p string) bool { return p == queued })
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "clip (3).mp4"); got != want {
		t.Fatalf("freeName = %q, want %q", got, want)
	}
	if category("clip.MP4") != "Video" || category("x.unknown") != "" {
		t.Fatal("category routing broken")
	}
}

func TestHelperFilesCannotBeClaimedByAnotherDownload(t *testing.T) {
	dir := t.TempDir()
	for _, pair := range [][2]string{
		{"file.bin", "file.bin.lock"}, {"file.bin", "file.bin.part"}, {"file.bin.part.meta", "file.bin"}, {"a", "a"},
	} {
		if !clashes(filepath.Join(dir, pair[0]), filepath.Join(dir, pair[1])) {
			t.Errorf("%s and %s should clash", pair[0], pair[1])
		}
	}
	if clashes(filepath.Join(dir, "a.bin"), filepath.Join(dir, "b.bin")) {
		t.Error("unrelated names clash")
	}
	// A stray lock file on disk also takes the name.
	if err := os.WriteFile(filepath.Join(dir, "x.zip.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := freeName(dir, "x.zip", func(string) bool { return false })
	if err != nil || filepath.Base(got) != "x (1).zip" {
		t.Fatalf("freeName = %q, %v", got, err)
	}
	for in, want := range map[string]string{"COM¹.txt": "_COM¹.txt", "lpt0": "_lpt0", "COM10.txt": "COM10.txt", "invoice‮fdp.exe": "invoice_fdp.exe"} {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}
