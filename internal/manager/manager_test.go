package manager_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/manager"
)

var errNotFound = errors.New("not found")

type memoryRepository struct {
	mu         sync.Mutex
	items      map[string]download.Item
	failUpdate bool
	updates    atomic.Int64
}

func newMemoryRepository(items ...download.Item) *memoryRepository {
	r := &memoryRepository{items: make(map[string]download.Item)}
	for _, item := range items {
		r.items[item.ID] = item
	}
	return r
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
		return download.Item{}, errNotFound
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
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items, nil
}

func (r *memoryRepository) Update(_ context.Context, item download.Item) error {
	r.updates.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failUpdate {
		return errors.New("disk full")
	}
	r.items[item.ID] = item
	return nil
}

func (r *memoryRepository) UpdateProgress(_ context.Context, id string, completed, total int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if ok && item.Status == download.StatusRunning {
		item.Completed, item.Total = completed, total
		r.items[id] = item
	}
	return nil
}

func (r *memoryRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return errNotFound
	}
	delete(r.items, id)
	return nil
}

func (r *memoryRepository) get(t *testing.T, id string) download.Item {
	t.Helper()
	item, err := r.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

type funcRunner struct {
	download  func(ctx context.Context, progress func(download.Progress)) error
	mu        sync.Mutex
	discarded []string
}

func (r *funcRunner) Download(ctx context.Context, _, _ string, progress func(download.Progress)) error {
	return r.download(ctx, progress)
}

func (r *funcRunner) Discard(destination string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discarded = append(r.discarded, destination)
	return nil
}

func newManager(t *testing.T, repo download.Repository, runner download.Runner, root string, publish func(download.Event)) (*manager.Manager, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	m, err := manager.New(repo, runner, manager.Options{
		MaxConcurrent: 1,
		Publish:       publish,
		DownloadRoots: []string{root},
		FlushInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return m, cancel, done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func seeded(id, destination string, status download.Status) download.Item {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	return download.Item{ID: id, URL: "https://example.com/" + id, Destination: destination, Status: status, CreatedAt: now, UpdatedAt: now}
}

func TestManagerQueuesRunsPublishesAndPauses(t *testing.T) {
	root := t.TempDir()
	repo := newMemoryRepository()
	started, canceled := make(chan struct{}), make(chan struct{})
	runner := &funcRunner{download: func(ctx context.Context, progress func(download.Progress)) error {
		progress(download.Progress{Completed: 32, Total: 64})
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}}
	events := make(chan download.Event, 16)
	m, _, _ := newManager(t, repo, runner, root, func(event download.Event) { events <- event })

	item, err := m.Add(context.Background(), "https://example.com/file.bin", filepath.Join(root, "file.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != download.StatusQueued {
		t.Fatalf("initial status = %q; want queued", item.Status)
	}
	<-started

	// Live progress is served from memory; the store sees it only on flush.
	items, err := m.List(context.Background())
	if err != nil || len(items) != 1 || items[0].Completed != 32 || items[0].Total != 64 {
		t.Fatalf("List = %+v, %v", items, err)
	}
	if stored := repo.get(t, item.ID); stored.Completed != 0 {
		t.Fatalf("progress reached the store before a flush: %+v", stored)
	}

	// Progress is published asynchronously by the Run loop.
	var sawRunning, sawProgress, sawPaused bool
	timeout := time.After(2 * time.Second)
	for !sawProgress {
		select {
		case event := <-events:
			sawRunning = sawRunning || (event.Type == download.EventUpdated && event.Item.Status == download.StatusRunning)
			sawProgress = event.Type == download.EventProgress && event.Item.Completed == 32
		case <-timeout:
			t.Fatal("no progress event published")
		}
	}

	if err := m.Pause(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	<-canceled
	if got := repo.get(t, item.ID); got.Status != download.StatusPaused || got.Completed != 32 || got.Total != 64 {
		t.Fatalf("paused item = %+v", got)
	}
	for len(events) > 0 {
		event := <-events
		sawPaused = sawPaused || (event.Type == download.EventUpdated && event.Item.Status == download.StatusPaused)
	}
	if !sawRunning || !sawPaused {
		t.Fatalf("events missing transitions: running=%v paused=%v", sawRunning, sawPaused)
	}
}

func TestManagerRejectsInvalidTransitions(t *testing.T) {
	root := t.TempDir()
	repo := newMemoryRepository(seeded("done", filepath.Join(root, "done.bin"), download.StatusCompleted))
	runner := &funcRunner{download: func(context.Context, func(download.Progress)) error { return nil }}
	m, _, _ := newManager(t, repo, runner, root, nil)

	if err := m.Pause(context.Background(), "done"); !errors.Is(err, manager.ErrInvalidTransition) {
		t.Fatalf("Pause(completed) = %v; want ErrInvalidTransition", err)
	}
	if err := m.Resume(context.Background(), "done"); !errors.Is(err, manager.ErrInvalidTransition) {
		t.Fatalf("Resume(completed) = %v; want ErrInvalidTransition", err)
	}
	if err := m.Retry(context.Background(), "done"); !errors.Is(err, manager.ErrInvalidTransition) {
		t.Fatalf("Retry(completed) = %v; want ErrInvalidTransition", err)
	}
	if got := repo.get(t, "done"); got.Status != download.StatusCompleted {
		t.Fatalf("completed item changed: %+v", got)
	}
}

func TestManagerRequeuesDownloadsInterruptedByACrash(t *testing.T) {
	root := t.TempDir()
	repo := newMemoryRepository(seeded("crashed", filepath.Join(root, "a.bin"), download.StatusRunning))
	runner := &funcRunner{download: func(_ context.Context, progress func(download.Progress)) error {
		progress(download.Progress{Completed: 10, Total: 10})
		return nil
	}}
	newManager(t, repo, runner, root, nil)
	waitFor(t, "recovered download to complete", func() bool {
		return repo.get(t, "crashed").Status == download.StatusCompleted
	})
}

func TestManagerShutdownWaitsForRunnersAndRequeues(t *testing.T) {
	root := t.TempDir()
	repo := newMemoryRepository(seeded("live", filepath.Join(root, "live.bin"), download.StatusQueued))
	started := make(chan struct{})
	runner := &funcRunner{download: func(ctx context.Context, progress func(download.Progress)) error {
		close(started)
		<-ctx.Done()
		// Simulate a final checkpoint that takes a moment after cancellation.
		time.Sleep(50 * time.Millisecond)
		progress(download.Progress{Completed: 7, Total: 100})
		return ctx.Err()
	}}
	_, cancel, done := newManager(t, repo, runner, root, nil)
	<-started
	cancel()
	<-done
	if got := repo.get(t, "live"); got.Status != download.StatusQueued || got.Completed != 7 {
		t.Fatalf("item after shutdown = %+v; want queued with final progress", got)
	}
}

func TestManagerFailedDownloadCanBeRetried(t *testing.T) {
	root := t.TempDir()
	var calls atomic.Int64
	runner := &funcRunner{download: func(context.Context, func(download.Progress)) error {
		if calls.Add(1) == 1 {
			return errors.New("HTTP 503")
		}
		return nil
	}}
	repo := newMemoryRepository()
	m, _, _ := newManager(t, repo, runner, root, nil)
	item, err := m.Add(context.Background(), "https://example.com/f", filepath.Join(root, "f"))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failure", func() bool { return repo.get(t, item.ID).Status == download.StatusFailed })
	if got := repo.get(t, item.ID); got.Error != "HTTP 503" {
		t.Fatalf("failed item = %+v", got)
	}
	if err := m.Retry(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completion after retry", func() bool { return repo.get(t, item.ID).Status == download.StatusCompleted })
}

func TestManagerDeleteRemovesOnlyOwnedFiles(t *testing.T) {
	root := t.TempDir()
	completed := filepath.Join(root, "completed.bin")
	foreign := filepath.Join(root, "foreign.bin")
	for _, path := range []string{completed, foreign} {
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repo := newMemoryRepository(
		seeded("completed", completed, download.StatusCompleted),
		seeded("failed", foreign, download.StatusFailed),
	)
	runner := &funcRunner{download: func(context.Context, func(download.Progress)) error { return nil }}
	events := make(chan download.Event, 8)
	m, _, _ := newManager(t, repo, runner, root, func(e download.Event) { events <- e })

	if err := m.Delete(context.Background(), "completed", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(completed); !os.IsNotExist(err) {
		t.Fatalf("completed destination not removed: %v", err)
	}
	if err := m.Delete(context.Background(), "failed", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("a failed item's destination may belong to the user and must survive: %v", err)
	}
	if len(runner.discarded) != 2 {
		t.Fatalf("partial state discarded for %v; want both items", runner.discarded)
	}
	if _, err := repo.Get(context.Background(), "completed"); !errors.Is(err, errNotFound) {
		t.Fatalf("completed item still stored: %v", err)
	}
	sawDeleted := false
	for len(events) > 0 {
		if (<-events).Type == download.EventDeleted {
			sawDeleted = true
		}
	}
	if !sawDeleted {
		t.Fatal("no deleted event published")
	}
}

func TestManagerDeleteRejectsActiveDownload(t *testing.T) {
	root := t.TempDir()
	started := make(chan struct{})
	runner := &funcRunner{download: func(ctx context.Context, _ func(download.Progress)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	repo := newMemoryRepository()
	m, _, _ := newManager(t, repo, runner, root, nil)
	item, err := m.Add(context.Background(), "https://example.com/f", filepath.Join(root, "f"))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := m.Delete(context.Background(), item.ID, false); !errors.Is(err, manager.ErrActive) {
		t.Fatalf("Delete(running) = %v; want ErrActive", err)
	}
}

func TestManagerConfinesDestinations(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "downloads")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner := &funcRunner{download: func(ctx context.Context, _ func(download.Progress)) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	m, _, _ := newManager(t, newMemoryRepository(), runner, root, nil)
	add := func(dest string) error {
		_, err := m.Add(context.Background(), "https://example.com/f", dest)
		return err
	}

	if err := add(filepath.Join(root, "new", "nested", "f.bin")); err != nil {
		t.Fatalf("nested destination inside root rejected: %v", err)
	}
	for name, dest := range map[string]string{
		"outside root": filepath.Join(outside, "f.bin"),
		"dot-dot":      filepath.Join(root, "..", "outside", "f.bin"),
		"relative":     "f.bin",
		"root itself":  root,
	} {
		if err := add(dest); err == nil {
			t.Errorf("%s: destination %q accepted", name, dest)
		}
	}
	if err := add(""); err == nil {
		t.Error("empty destination accepted")
	}
	if _, err := m.Add(context.Background(), "file:///etc/passwd", filepath.Join(root, "p")); err == nil {
		t.Error("non-HTTP URL accepted")
	}

	if runtime.GOOS == "windows" {
		for _, alias := range []string{`Startup.`, `f.bin::$DATA`, `dir \f.bin`} {
			if err := add(filepath.Join(root, alias)); err == nil {
				t.Errorf("Windows alias %q accepted", alias)
			}
		}
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Logf("symlinks unavailable, skipping escape check: %v", err)
		return
	}
	if err := add(filepath.Join(link, "f.bin")); err == nil {
		t.Error("destination escaping through a symlink accepted")
	}
}

func TestManagerDoesNotSpinWhenTheStoreFails(t *testing.T) {
	root := t.TempDir()
	repo := newMemoryRepository(seeded("stuck", filepath.Join(root, "s"), download.StatusQueued))
	repo.failUpdate = true
	runner := &funcRunner{download: func(context.Context, func(download.Progress)) error { return nil }}
	newManager(t, repo, runner, root, nil)
	time.Sleep(200 * time.Millisecond)
	if n := repo.updates.Load(); n > 3 {
		t.Fatalf("store Update called %d times in 200ms; scheduler is spinning", n)
	}
}

func TestManagerPauseRacingCompletionKeepsTheFinishedDownload(t *testing.T) {
	root := t.TempDir()
	started, release := make(chan struct{}), make(chan struct{})
	runner := &funcRunner{download: func(context.Context, func(download.Progress)) error {
		close(started)
		<-release // the file is already renamed into place; cancellation comes too late
		return nil
	}}
	repo := newMemoryRepository()
	m, _, _ := newManager(t, repo, runner, root, nil)
	item, err := m.Add(context.Background(), "https://example.com/f", filepath.Join(root, "f"))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := m.Pause(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitFor(t, "completion despite the pause", func() bool {
		return repo.get(t, item.ID).Status == download.StatusCompleted
	})
}

func TestManagerRejectsASecondDownloadToTheSameDestination(t *testing.T) {
	root := t.TempDir()
	runner := &funcRunner{download: func(ctx context.Context, _ func(download.Progress)) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	m, _, _ := newManager(t, newMemoryRepository(), runner, root, nil)
	dest := filepath.Join(root, "same.bin")
	if _, err := m.Add(context.Background(), "https://example.com/a", dest); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add(context.Background(), "https://example.com/b", dest); !errors.Is(err, download.ErrInvalidDownload) {
		t.Fatalf("second Add to the same destination = %v; want ErrInvalidDownload", err)
	}
}

func TestManagerDeletesAnOrphanedRunningRow(t *testing.T) {
	root := t.TempDir()
	repo := newMemoryRepository(seeded("orphan", filepath.Join(root, "o"), download.StatusRunning))
	runner := &funcRunner{download: func(context.Context, func(download.Progress)) error { return nil }}
	m, err := manager.New(repo, runner, manager.Options{DownloadRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	// No runner exists for the row, so it must not be stuck behind ErrActive.
	if err := m.Delete(context.Background(), "orphan", false); err != nil {
		t.Fatalf("Delete(orphaned running row) = %v", err)
	}
}

func TestManagerRequeuesWhenTheDestinationIsBusy(t *testing.T) {
	root := t.TempDir()
	runner := &funcRunner{download: func(context.Context, func(download.Progress)) error {
		return fmt.Errorf("%w: locked", download.ErrDestinationBusy)
	}}
	repo := newMemoryRepository()
	m, _, _ := newManager(t, repo, runner, root, nil)
	item, err := m.Add(context.Background(), "https://example.com/f", filepath.Join(root, "f"))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the busy attempt to finish", func() bool { return repo.updates.Load() >= 2 })
	time.Sleep(50 * time.Millisecond)
	if got := repo.get(t, item.ID); got.Status != download.StatusQueued || got.Error != "" {
		t.Fatalf("item after a busy destination = %+v; want queued for a later retry", got)
	}
}
