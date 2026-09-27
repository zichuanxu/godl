package quarantine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMarkSetsQuarantineAttribute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Mark(path, "https://example.com/tool"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	n, err := unix.Getxattr(path, "com.apple.quarantine", buf)
	if err != nil || !strings.HasSuffix(string(buf[:n]), ";nimget;") {
		t.Fatalf("xattr = %q, %v", buf[:n], err)
	}
}
