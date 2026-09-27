package engine

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestIntervalCheckpointRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.part.meta")
	state := intervalCheckpoint{
		Version:   1,
		Identity:  "object-v1",
		Size:      100,
		ETag:      `"v1"`,
		Remaining: []byteRange{{Start: 25, End: 49}, {Start: 75, End: 99}},
		Completed: 50,
	}
	if err := writeIntervalCheckpoint(path, state); err != nil {
		t.Fatal(err)
	}
	got, err := readIntervalCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, state) {
		t.Fatalf("checkpoint = %+v; want %+v", got, state)
	}
}

func TestIntervalCheckpointRejectsOverlapAndInconsistentCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.meta")
	state := intervalCheckpoint{
		Version:   1,
		Identity:  "object-v1",
		Size:      100,
		ETag:      `"v1"`,
		Remaining: []byteRange{{Start: 0, End: 60}, {Start: 50, End: 99}},
		Completed: 0,
	}
	if err := writeIntervalCheckpoint(path, state); err == nil {
		t.Fatal("invalid checkpoint unexpectedly written")
	}
}
