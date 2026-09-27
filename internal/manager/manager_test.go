package manager_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/manager"
)

type memoryRepository struct {
	mu    sync.Mutex
	items map[string]download.Item
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{items: make(map[string]download.Item)}
}

func (r *memoryRepository) Create(_ context.Context, item download.Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[item.ID] = item
	return nil
}

func (r *memoryRepository) Get(_ context.Context, id string) (download.Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok {
		return download.Item{}, errors.New("not found")
	}
	return item, nil
}

func (r *memoryRepository) List(context.Context) ([]download.Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]download.Item, 0, len(r.items))
	for _, item := range r.items {
		items = append(items, item)
	}
	return items, nil
}

func (r *memoryRepository) Update(_ context.Context, item download.Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[item.ID] = item
	return nil
}

type blockingRunner struct {
	started chan struct{}
	cancel  chan struct{}
}

func (r *blockingRunner) Download(ctx context.Context, _, _ string, progress func(download.Progress)) error {
	progress(download.Progress{Completed: 32, Total: 64})
	close(r.started)
	<-ctx.Done()
	close(r.cancel)
	return ctx.Err()
}

func TestManagerQueuesRunsPublishesAndPauses(t *testing.T) {
	repo := newMemoryRepository()
	runner := &blockingRunner{started: make(chan struct{}), cancel: make(chan struct{})}
	events := make(chan download.Event, 8)

	m, err := manager.New(repo, runner, manager.Options{
		MaxConcurrent: 1,
		Publish:       func(event download.Event) { events <- event },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Run(ctx)

	item, err := m.Add(context.Background(), "https://example.com/file.bin", "/tmp/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != download.StatusQueued {
		t.Fatalf("initial status = %q; want queued", item.Status)
	}

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("download did not start")
	}

	if err := m.Pause(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.cancel:
	case <-time.After(time.Second):
		t.Fatal("pause did not cancel the active download")
	}

	got, err := repo.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != download.StatusPaused || got.Completed != 32 || got.Total != 64 {
		t.Fatalf("paused item = %+v", got)
	}

	seenRunning := false
	seenProgress := false
	seenPaused := false
	for len(events) > 0 {
		event := <-events
		switch event.Type {
		case download.EventUpdated:
			seenRunning = seenRunning || event.Item.Status == download.StatusRunning
			seenProgress = seenProgress || event.Item.Completed == 32
			seenPaused = seenPaused || event.Item.Status == download.StatusPaused
		}
	}
	if !seenRunning || !seenProgress || !seenPaused {
		t.Fatalf("events missing lifecycle transitions: running=%v progress=%v paused=%v", seenRunning, seenProgress, seenPaused)
	}
}
