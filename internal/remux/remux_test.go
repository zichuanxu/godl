package remux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindAndOutput(t *testing.T) {
	if _, err := Find(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrNoFFmpeg) {
		t.Fatalf("missing configured ffmpeg: %v", err)
	}
	if got := Output("/d/video.ts"); got != "/d/video.mp4" {
		t.Fatalf("Output = %q", got)
	}
}

// fakeFFmpeg writes a script that copies its input to its last argument, or
// fails, standing in for ffmpeg.
func fakeFFmpeg(t *testing.T, fail bool) string {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	body := "#!/bin/sh\nfor last; do :; done\nin=\"\"\nwhile [ $# -gt 0 ]; do [ \"$1\" = -i ] && in=\"$2\"; shift; done\ncp \"$in\" \"$last\"\n"
	if fail {
		body = "#!/bin/sh\necho 'Invalid data found' >&2\nexit 1\n"
	}
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestToMP4(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "clip.ts")
	if err := os.WriteFile(in, []byte("stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := ToMP4(context.Background(), fakeFFmpeg(t, false), in)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(out); string(data) != "stream" || out != filepath.Join(dir, "clip.mp4") {
		t.Fatalf("out %q = %q", out, data)
	}
	if _, err := ToMP4(context.Background(), fakeFFmpeg(t, false), in); err == nil {
		t.Fatal("existing MP4 overwritten")
	}
	in2 := filepath.Join(dir, "bad.ts")
	_ = os.WriteFile(in2, []byte("x"), 0o600)
	if _, err := ToMP4(context.Background(), fakeFFmpeg(t, true), in2); err == nil {
		t.Fatal("ffmpeg failure not reported")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed run left an MP4")
	}
}

// With a real ffmpeg, remux a generated MPEG-TS clip.
func TestToMP4WithRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "gen.ts")
	gen := exec.Command(ffmpeg, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=10", "-c:v", "mpeg2video", in)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("cannot generate a test clip: %v %s", err, out)
	}
	out, err := ToMP4(context.Background(), ffmpeg, in)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		t.Fatalf("mp4 missing: %v", err)
	}
}
