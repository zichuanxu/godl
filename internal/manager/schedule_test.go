package manager_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/manager"
	"github.com/zichuanxu/godl/internal/settings"
	"golang.org/x/time/rate"
)

// fakeClock is the injectable scheduler clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(hhmm string) {
	t, err := time.ParseInLocation("15:04", hhmm, time.Local)
	if err != nil {
		panic(err)
	}
	c.mu.Lock()
	c.now = time.Date(2026, 9, 27, t.Hour(), t.Minute(), 0, 0, time.Local)
	c.mu.Unlock()
}

// blockingRunner holds every download until its context ends and records
// the specs it was started with.
func blockingRunner() *funcRunner {
	return &funcRunner{download: func(ctx context.Context, _ func(download.Progress)) error {
		<-ctx.Done()
		return ctx.Err()
	}}
}

func (r *funcRunner) started() []download.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]download.Spec(nil), r.specs...)
}

type harness struct {
	m      *manager.Manager
	repo   *memoryRepository
	runner *funcRunner
	clock  *fakeClock
	root   string
}

func queued(id, url string, priority int, age time.Duration) download.Item {
	created := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).Add(-age)
	return download.Item{ID: id, URL: url, Destination: filepath.Join(tempRoot, id), Status: download.StatusQueued, Priority: priority, CreatedAt: created, UpdatedAt: created}
}

// tempRoot is the current test's download root, set before its items are
// built; tests in this package do not run in parallel.
var tempRoot string

func newHarness(t *testing.T, s settings.Settings, build func() []download.Item) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tempRoot = root
	var items []download.Item
	if build != nil {
		items = build()
	}
	h := &harness{repo: newMemoryRepository(items...), runner: blockingRunner(), clock: &fakeClock{}, root: root}
	h.clock.Set("12:00")
	m, err := manager.New(h.repo, h.runner, manager.Options{
		Defaults: s, DownloadRoots: []string{root}, Now: h.clock.Now, FlushInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.m = m
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
	return h
}

func TestSchedulerStartsHighestPriorityThenOldest(t *testing.T) {
	s := settings.Default()
	s.MaxConcurrent = 1
	h := newHarness(t, s, func() []download.Item {
		return []download.Item{
			queued("old-normal", "https://a.test/1", download.PriorityNormal, 3*time.Hour),
			queued("new-high", "https://a.test/2", download.PriorityHigh, time.Hour),
			queued("old-high", "https://a.test/3", download.PriorityHigh, 2*time.Hour),
			queued("oldest-low", "https://a.test/4", download.PriorityLow, 4*time.Hour),
		}
	})
	want := []string{"old-high", "new-high", "old-normal", "oldest-low"}
	for i, id := range want {
		waitFor(t, id+" to start", func() bool { return h.repo.get(t, id).Status == download.StatusRunning })
		if got := h.runner.started(); len(got) != i+1 {
			t.Fatalf("%d downloads started, want %d", len(got), i+1)
		}
		if err := h.m.Pause(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHostCapSharesConnectionsAcrossDownloads(t *testing.T) {
	s := settings.Default()
	s.MaxConcurrent = 4
	s.Connections = 8
	s.HostConnections = 10
	s.Sites = []settings.Site{{Host: "tiny.test", HostConnections: 1, Headers: map[string]string{"X-Site": "yes"}}}
	h := newHarness(t, s, func() []download.Item {
		return []download.Item{
			queued("a1", "https://a.test/1", 0, 4*time.Hour),
			queued("a2", "https://A.test/2", 0, 3*time.Hour),
			queued("a3", "https://a.test/3", 0, 2*time.Hour),
			queued("t1", "https://cdn.tiny.test/1", 0, time.Hour),
		}
	})
	waitFor(t, "three downloads to start", func() bool { return len(h.runner.started()) == 3 })
	time.Sleep(30 * time.Millisecond) // several scheduler ticks
	specs := h.runner.started()
	if len(specs) != 3 {
		t.Fatalf("%d downloads started, want 3 (a3 must wait for a.test connections)", len(specs))
	}
	conns := map[string]int{}
	for _, spec := range specs {
		conns[spec.URL] = spec.Connections
		if spec.URL == "https://cdn.tiny.test/1" && spec.Headers.Get("X-Site") != "yes" {
			t.Errorf("site header missing from %v", spec.Headers)
		}
	}
	if conns["https://a.test/1"] != 8 || conns["https://A.test/2"] != 2 || conns["https://cdn.tiny.test/1"] != 1 {
		t.Fatalf("connections = %v, want a1=8 a2=2 t1=1", conns)
	}
	if h.repo.get(t, "a3").Status != download.StatusQueued {
		t.Fatal("a3 started beyond the host cap")
	}
	// Releasing a1's connections lets a3 start with them.
	if err := h.m.Pause(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a3 to start", func() bool { return h.repo.get(t, "a3").Status == download.StatusRunning })
	last := h.runner.started()[3]
	if last.URL != "https://a.test/3" || last.Connections != 8 {
		t.Fatalf("a3 started with %+v", last)
	}
}

func TestScheduleWindowAndSpeedRulesFollowTheClock(t *testing.T) {
	s := settings.Default()
	s.SpeedLimit = 1000
	s.Schedule = settings.Schedule{
		Start: "01:00", Stop: "07:00",
		SpeedRules: []settings.SpeedRule{{From: "06:00", To: "07:00", Limit: 50}},
	}
	h := newHarness(t, s, func() []download.Item {
		return []download.Item{queued("night", "https://a.test/n", 0, time.Hour)}
	})
	time.Sleep(30 * time.Millisecond)
	if len(h.runner.started()) != 0 {
		t.Fatal("download started outside the queue window")
	}

	h.clock.Set("01:00")
	waitFor(t, "the window to open", func() bool { return h.repo.get(t, "night").Status == download.StatusRunning })
	global := h.runner.started()[0].Limiters[0]
	waitFor(t, "the default global limit", func() bool { return global.Limit() == 1000 })

	h.clock.Set("06:30")
	waitFor(t, "the speed rule", func() bool { return global.Limit() == 50 })

	h.clock.Set("07:00")
	waitFor(t, "the window to close", func() bool { return h.repo.get(t, "night").Status == download.StatusQueued })
	time.Sleep(30 * time.Millisecond)
	if n := len(h.runner.started()); n != 1 {
		t.Fatalf("download restarted after the window closed (%d starts)", n)
	}

	// Removing the schedule reopens the queue and lifts the limit.
	s.Schedule = settings.Schedule{}
	s.SpeedLimit = 0
	if err := h.m.UpdateSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the queue to reopen", func() bool { return len(h.runner.started()) == 2 })
	if global.Limit() != rate.Inf {
		t.Fatalf("global limit = %v, want unlimited", global.Limit())
	}
	stored, ok, _ := h.repo.LoadSettings(context.Background())
	if !ok || stored.Schedule.Start != "" {
		t.Fatal("settings were not persisted")
	}
	s.MaxConcurrent = 0
	if err := h.m.UpdateSettings(context.Background(), s); !errors.Is(err, download.ErrInvalidSettings) {
		t.Fatalf("invalid settings: err = %v", err)
	}
}

func TestUpdateChangesRunningSpeedLimitAndPriority(t *testing.T) {
	h := newHarness(t, settings.Default(), func() []download.Item {
		return []download.Item{queued("x", "https://a.test/x", 0, time.Hour)}
	})
	waitFor(t, "start", func() bool { return len(h.runner.started()) == 1 })
	own := h.runner.started()[0].Limiters[1]
	if own.Limit() != rate.Inf {
		t.Fatalf("unlimited download has limit %v", own.Limit())
	}
	limit, priority := int64(4096), download.PriorityHigh
	if err := h.m.Update(context.Background(), "x", download.Patch{SpeedLimit: &limit, Priority: &priority}); err != nil {
		t.Fatal(err)
	}
	if own.Limit() != 4096 {
		t.Fatalf("live limit = %v, want 4096", own.Limit())
	}
	if item := h.repo.get(t, "x"); item.SpeedLimit != 4096 || item.Priority != 1 {
		t.Fatalf("stored item = %+v", item)
	}
	bad := 5
	if err := h.m.Update(context.Background(), "x", download.Patch{Priority: &bad}); !errors.Is(err, download.ErrInvalidDownload) {
		t.Fatalf("bad priority: err = %v", err)
	}
}

func TestAddNamesFromServerAndAvoidsCollisions(t *testing.T) {
	s := settings.Default()
	s.MaxConcurrent = 1
	h := newHarness(t, s, nil)
	h.runner.remote = download.Remote{Filename: "../Report.pdf", URL: "https://cdn.test/opaque"}
	first, err := h.m.Add(context.Background(), download.Request{URL: "https://a.test/get?id=1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(h.root, "Documents", "_Report.pdf"); first.Destination != want {
		t.Fatalf("destination = %q, want %q", first.Destination, want)
	}
	second, err := h.m.Add(context.Background(), download.Request{URL: "https://a.test/get?id=2"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(second.Destination) != "_Report (1).pdf" {
		t.Fatalf("second destination = %q", second.Destination)
	}
	h.runner.remote = download.Remote{}
	dir := filepath.Join(h.root, "picked")
	third, err := h.m.Add(context.Background(), download.Request{URL: "https://a.test/files/movie.mkv", Directory: dir, Priority: download.PriorityHigh})
	if err != nil {
		t.Fatal(err)
	}
	if third.Destination != filepath.Join(dir, "movie.mkv") || third.Priority != 1 {
		t.Fatalf("third = %+v", third)
	}
}
