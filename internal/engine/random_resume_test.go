package engine

import (
	"math/rand/v2"
	"path/filepath"
	"testing"
)

func TestRandomizedIntervalCheckpointResume(t *testing.T) {
	random := rand.New(rand.NewPCG(42, 99))
	for iteration := 0; iteration < 100; iteration++ {
		size := int64(1024 + random.IntN(4096))
		scheduler, err := newIntervalScheduler(size, 64)
		if err != nil {
			t.Fatal(err)
		}
		var assigned []byteRange
		for len(assigned) < 8 {
			rangeToAssign, assignErr := scheduler.Assign()
			if assignErr != nil {
				break
			}
			assigned = append(assigned, rangeToAssign)
		}
		completed := assigned[random.IntN(len(assigned))]
		if err := scheduler.Complete(completed); err != nil {
			t.Fatal(err)
		}
		remaining := append([]byteRange(nil), scheduler.available...)
		for _, current := range assigned {
			if current != completed {
				remaining = append(remaining, current)
			}
		}
		state := intervalCheckpoint{
			Version:   1,
			Identity:  "random",
			Size:      size,
			Remaining: remaining,
			Completed: scheduler.completed,
		}
		path := filepath.Join(t.TempDir(), "resume.meta")
		if err := writeIntervalCheckpoint(path, state); err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		if _, err := readIntervalCheckpoint(path); err != nil {
			t.Fatalf("iteration %d reload: %v", iteration, err)
		}
	}
}
