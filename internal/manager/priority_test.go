package manager

import (
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/download"
)

func TestPriorityQueueOrdersPriorityThenFIFO(t *testing.T) {
	base := time.Unix(100, 0)
	queue := priorityQueue{}
	queue.Push(download.Item{ID: "normal", Status: download.StatusQueued, CreatedAt: base, UpdatedAt: base}, PriorityNormal)
	queue.Push(download.Item{ID: "high-late", Status: download.StatusQueued, CreatedAt: base.Add(time.Second), UpdatedAt: base.Add(time.Second)}, PriorityHigh)
	queue.Push(download.Item{ID: "high-early", Status: download.StatusQueued, CreatedAt: base, UpdatedAt: base}, PriorityHigh)

	for _, want := range []string{"high-early", "high-late", "normal"} {
		got, ok := queue.Pop()
		if !ok || got.ID != want {
			t.Fatalf("Pop = %+v, %v; want %q", got, ok, want)
		}
	}
	if _, ok := queue.Pop(); ok {
		t.Fatal("empty priority queue unexpectedly returned an item")
	}
}
