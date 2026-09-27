package engine

import "testing"

func TestIntervalSchedulerSplitsLargestRemainingRange(t *testing.T) {
	scheduler, err := newIntervalScheduler(100, 10)
	if err != nil {
		t.Fatal(err)
	}

	first, err := scheduler.Assign()
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.Assign()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first.Overlaps(second) {
		t.Fatalf("assigned overlapping ranges: %+v and %+v", first, second)
	}

	if err := scheduler.Complete(first); err != nil {
		t.Fatal(err)
	}
	third, err := scheduler.Assign()
	if err != nil {
		t.Fatal(err)
	}
	if third.Overlaps(second) || third.Overlaps(first) {
		t.Fatalf("split range overlaps existing range: %+v", third)
	}
	if third.Length() != 12 {
		t.Fatalf("largest remaining split length = %d; want 12", third.Length())
	}
	if got := scheduler.CompletedBytes(); got != first.Length() {
		t.Fatalf("completed bytes = %d; want %d", got, first.Length())
	}
}

func TestIntervalSchedulerRejectsInvalidCompletion(t *testing.T) {
	scheduler, err := newIntervalScheduler(100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Complete(byteRange{Start: 100, End: 101}); err == nil {
		t.Fatal("invalid completion unexpectedly succeeded")
	}
	if _, err := newIntervalScheduler(0, 10); err == nil {
		t.Fatal("zero size unexpectedly accepted")
	}
}
