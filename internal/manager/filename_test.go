package manager

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDestinationRoutesAndAvoidsCollisions(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Video"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Video", "clip.mp4"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveDestination(root, "https://example.com/clip.mp4", "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "Video", "clip (1).mp4")
	if got != want {
		t.Fatalf("destination = %q; want %q", got, want)
	}
}

func TestResolveDestinationSanitizesPathTraversal(t *testing.T) {
	got, err := resolveDestination(t.TempDir(), "https://example.com/%2e%2e/%2e%2e/CON.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) == "CON.txt" || filepath.Base(got) == ".." || filepath.Base(got) == "." {
		t.Fatalf("unsafe filename = %q", got)
	}
}
