package manager

import (
	"context"
	"testing"
	"time"
)

func TestHierarchicalLimiterUsesGlobalAndDownloadBuckets(t *testing.T) {
	limiter := newHierarchicalLimiter(100, 100)
	ctx := context.Background()
	if err := limiter.WaitN(ctx, "download-1", 50); err != nil {
		t.Fatal(err)
	}
	if err := limiter.WaitN(ctx, "download-2", 50); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := limiter.WaitN(ctx, "download-1", 1); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("limiter blocked beyond bounded refill window")
	}
}
