package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFileKeepsBoundedCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godl.log")
	r, err := OpenRotating(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n"
	for range 20 {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.Close()
	for _, p := range []string{path, path + ".1", path + ".2", path + ".3"} {
		fi, err := os.Stat(p)
		if err != nil || fi.Size() > 100 {
			t.Fatalf("%s: %v size %d", p, err, fi.Size())
		}
	}
	if _, err := os.Stat(path + ".4"); err == nil {
		t.Fatal("more copies kept than asked")
	}
}
