package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreallocateCreatesRequestedFileSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.part")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	if err := preallocate(file, 1<<20); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 1<<20 {
		t.Fatalf("file size = %d; want %d", info.Size(), 1<<20)
	}
}
