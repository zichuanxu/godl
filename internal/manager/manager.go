// Package manager owns the ordered service download queue.
package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/logging"
)

var (
	ErrInvalidTransition = download.ErrInvalidTransition
	ErrActive            = download.ErrActive
)

// busyRetryDelay is how long a download whose destination is locked by
// another process waits before the scheduler tries it again.
const busyRetryDelay = 5 * time.Second

type Options struct {
	MaxConcurrent int
	Publish       func(download.Event)
	Now           func() time.Time
	Logger        *slog.Logger
	// DownloadRoots lists the directories destinations must resolve inside.
	DownloadRoots []string
	// FlushInterval is how often in-memory progress is persisted, orphaned
	// running rows are requeued, and the scheduler retries after a store
	// error. It defaults to 5s.
	FlushInterval time.Duration
}

// job is the in-memory state of a started runner. item mirrors the stored row
// and is guarded by Manager.mu. The engine reports progress into the atomics
// without taking any manager lock, so a slow SQLite write can never stall the
// engine; the Run loop copies it into item and publishes it.
type job struct {
	cancel    context.CancelFunc
	item      download.Item
	completed atomic.Int64
	total     atomic.Int64
}

func (j *job) progress() (completed, total int64) {
	return j.completed.Load(), j.total.Load()
}

type Manager struct {
	repo   download.Repository
	runner download.Runner
	opts   Options
	roots  []string
	log    *slog.Logger

	wake         chan struct{}
	progressWake chan struct{}
	wg           sync.WaitGroup

	// mu serializes status transitions, guards active, retryAt, and job
	// items, and orders published events with the state they describe.
	// Publish must not block.
	mu      sync.Mutex
	active  map[string]*job
	retryAt map[string]time.Time
}

func New(repo download.Repository, runner download.Runner, opts Options) (*Manager, error) {
	if repo == nil {
		return nil, errors.New("nil download repository")
	}
	if runner == nil {
		return nil, errors.New("nil download runner")
	}
	if opts.MaxConcurrent == 0 {
		opts.MaxConcurrent = 3
	}
	if opts.MaxConcurrent < 1 {
		return nil, errors.New("max concurrent downloads must be positive")
	}
	if opts.Publish == nil {
		opts.Publish = func(download.Event) {}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = 5 * time.Second
	}
	roots, err := resolveRoots(opts.DownloadRoots)
	if err != nil {
		return nil, err
	}
	return &Manager{
		repo: repo, runner: runner, opts: opts, roots: roots, log: opts.Logger,
		wake: make(chan struct{}, 1), progressWake: make(chan struct{}, 1),
		active: make(map[string]*job), retryAt: make(map[string]time.Time),
	}, nil
}

func (m *Manager) Add(ctx context.Context, rawURL, destination string) (download.Item, error) {
	if err := validateURL(rawURL); err != nil {
		return download.Item{}, fmt.Errorf("%w: %w", download.ErrInvalidDownload, err)
	}
	destination, err := confine(m.roots, destination)
	if err != nil {
		return download.Item{}, fmt.Errorf("%w: %w", download.ErrInvalidDownload, err)
	}
	id, err := newID()
	if err != nil {
		return download.Item{}, err
	}
	now := m.opts.Now().UTC()
	item := download.Item{
		ID: id, URL: rawURL, Destination: destination, Status: download.StatusQueued,
		CreatedAt: now, UpdatedAt: now,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Two items sharing a destination would share .part files, and deleting
	// one with its files would destroy the other's data.
	items, err := m.repo.List(ctx)
	if err != nil {
		return download.Item{}, err
	}
	for _, existing := range items {
		if samePath(existing.Destination, destination) {
			return download.Item{}, fmt.Errorf("%w: destination %s is already used by download %s", download.ErrInvalidDownload, destination, existing.ID)
		}
	}
	if err := m.repo.Create(ctx, item); err != nil {
		return download.Item{}, err
	}
	m.log.Info("download added", "id", id, "url", logging.RedactURL(rawURL))
	m.publish(download.EventUpdated, item)
	m.signal()
	return item, nil
}

// List returns stored items with live progress applied.
func (m *Manager) List(ctx context.Context) ([]download.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := m.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if j, ok := m.active[items[i].ID]; ok {
			items[i].Completed, items[i].Total = j.progress()
		}
	}
	return items, nil
}

func (m *Manager) Pause(ctx context.Context, id string) error {
	return m.transition(ctx, id, func(item *download.Item) error {
		if item.Status != download.StatusQueued && item.Status != download.StatusRunning {
			return fmt.Errorf("%w: cannot pause a %s download", ErrInvalidTransition, item.Status)
		}
		if j, ok := m.active[item.ID]; ok {
			j.cancel()
			j.item.Status = download.StatusPaused
			item.Completed, item.Total = j.progress()
		}
		item.Status = download.StatusPaused
		item.Error = ""
		return nil
	})
}

func (m *Manager) Resume(ctx context.Context, id string) error {
	return m.requeue(ctx, id, download.StatusPaused)
}

func (m *Manager) Retry(ctx context.Context, id string) error {
	return m.requeue(ctx, id, download.StatusFailed)
}

func (m *Manager) requeue(ctx context.Context, id string, from download.Status) error {
	err := m.transition(ctx, id, func(item *download.Item) error {
		if item.Status != from {
			return fmt.Errorf("%w: expected a %s download, got %s", ErrInvalidTransition, from, item.Status)
		}
		// A paused runner may still be shutting down; requeueing now would let
		// the scheduler start a second runner against the same destination.
		if _, ok := m.active[item.ID]; ok {
			return ErrActive
		}
		item.Status = download.StatusQueued
		item.Error = ""
		return nil
	})
	if err == nil {
		m.signal()
	}
	return err
}

// transition applies change to the stored item under the manager lock, then
// persists and publishes it.
func (m *Manager) transition(ctx context.Context, id string, change func(*download.Item) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := change(&item); err != nil {
		return err
	}
	item.UpdatedAt = m.opts.Now().UTC()
	if err := m.repo.Update(ctx, item); err != nil {
		return err
	}
	m.publish(download.EventUpdated, item)
	return nil
}

// Delete removes a download whose runner has stopped. With removeFiles it also
// discards partial state and, for completed downloads only, the destination
// file; a failed item's destination may be a pre-existing user file and is
// never touched.
func (m *Manager) Delete(ctx context.Context, id string, removeFiles bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	// A row can read "running" with no runner after a failed store write;
	// only a live runner blocks deletion.
	if _, ok := m.active[id]; ok {
		return ErrActive
	}
	if err := m.repo.Delete(ctx, id); err != nil {
		return err
	}
	delete(m.retryAt, id)
	m.publish(download.EventDeleted, item)
	m.log.Info("download deleted", "id", id, "removeFiles", removeFiles)
	if !removeFiles {
		return nil
	}
	// Re-check confinement: a symlinked ancestor may have changed since Add.
	if _, err := confine(m.roots, item.Destination); err != nil {
		return fmt.Errorf("download deleted, but its files were kept: %w", err)
	}
	errs := []error{m.runner.Discard(item.Destination)}
	if item.Status == download.StatusCompleted {
		if err := os.Remove(item.Destination); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("download deleted, but removing its files failed: %w", err)
	}
	return nil
}

// Run schedules downloads until ctx is canceled, then cancels every runner and
// waits for them to record their final state before returning.
func (m *Manager) Run(ctx context.Context) {
	m.requeueOrphans(ctx)
	ticker := time.NewTicker(m.opts.FlushInterval)
	defer ticker.Stop()
	m.signal()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			m.wg.Wait()
			return
		case <-ticker.C:
			m.flush(ctx)
			m.requeueOrphans(ctx)
			m.schedule(ctx)
		case <-m.progressWake:
			m.publishProgress()
		case <-m.wake:
			m.schedule(ctx)
		}
	}
}

// requeueOrphans requeues rows marked running that have no runner: rows a
// crashed process left behind, and rows whose final store write failed.
func (m *Manager) requeueOrphans(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, err := m.repo.List(ctx)
	if err != nil {
		m.log.Error("list downloads for recovery", "err", err)
		return
	}
	for _, item := range items {
		if _, ok := m.active[item.ID]; ok || item.Status != download.StatusRunning {
			continue
		}
		item.Status = download.StatusQueued
		item.UpdatedAt = m.opts.Now().UTC()
		if err := m.repo.Update(ctx, item); err != nil {
			m.log.Error("requeue interrupted download", "id", item.ID, "err", err)
			continue
		}
		m.log.Info("requeued interrupted download", "id", item.ID)
		m.publish(download.EventUpdated, item)
	}
}

func (m *Manager) schedule(ctx context.Context) {
	for ctx.Err() == nil {
		m.mu.Lock()
		capacity := m.opts.MaxConcurrent - len(m.active)
		m.mu.Unlock()
		if capacity <= 0 {
			return
		}
		items, err := m.repo.List(ctx)
		if err != nil {
			m.log.Error("list downloads for scheduling", "err", err)
			return
		}
		started := false
		for _, item := range items {
			if item.Status != download.StatusQueued {
				continue
			}
			ok, err := m.start(ctx, item.ID)
			if err != nil {
				// Retried on the next tick rather than in a hot loop.
				m.log.Error("start download", "id", item.ID, "err", err)
				return
			}
			if ok {
				started = true
				break
			}
		}
		if !started {
			return
		}
	}
}

func (m *Manager) start(ctx context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.active[id]; ok {
		return false, nil
	}
	if at, ok := m.retryAt[id]; ok && m.opts.Now().Before(at) {
		return false, nil
	}
	delete(m.retryAt, id)
	item, err := m.repo.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if item.Status != download.StatusQueued {
		return false, nil
	}
	item.UpdatedAt = m.opts.Now().UTC()
	// Re-check confinement: a symlinked ancestor may have changed since Add.
	if _, err := confine(m.roots, item.Destination); err != nil {
		item.Status = download.StatusFailed
		item.Error = err.Error()
		if err := m.repo.Update(ctx, item); err != nil {
			return false, err
		}
		m.publish(download.EventUpdated, item)
		return false, nil
	}
	item.Status = download.StatusRunning
	item.Error = ""
	if err := m.repo.Update(ctx, item); err != nil {
		return false, err
	}
	workCtx, cancel := context.WithCancel(ctx)
	j := &job{cancel: cancel, item: item}
	j.completed.Store(item.Completed)
	j.total.Store(item.Total)
	m.active[id] = j
	m.wg.Add(1)
	m.log.Info("download started", "id", id, "url", logging.RedactURL(item.URL))
	m.publish(download.EventUpdated, item)
	go m.run(workCtx, j)
	return true, nil
}

func (m *Manager) run(ctx context.Context, j *job) {
	defer m.wg.Done()
	err := m.runner.Download(ctx, j.item.URL, j.item.Destination, func(p download.Progress) {
		j.completed.Store(p.Completed)
		j.total.Store(p.Total)
		select {
		case m.progressWake <- struct{}{}:
		default:
		}
	})
	canceled := ctx.Err() != nil
	j.cancel()
	m.finish(j, err, canceled)
}

func (m *Manager) finish(j *job, runErr error, canceled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := j.item.ID
	delete(m.active, id)
	defer m.signal()

	item, err := m.repo.Get(context.Background(), id)
	if err != nil {
		m.log.Error("load finished download", "id", id, "err", err)
		return
	}
	item.Completed, item.Total = j.progress()
	item.UpdatedAt = m.opts.Now().UTC()
	switch {
	case runErr == nil:
		// Checked first: a Pause that raced the final rename must not hide a
		// finished file, which a later Resume would reject as already existing.
		item.Status = download.StatusCompleted
		item.Error = ""
		if item.Total > 0 {
			item.Completed = item.Total
		} else {
			item.Total = item.Completed
		}
	case item.Status != download.StatusRunning:
		// Paused while running: keep the user's status, record final bytes.
	case errors.Is(runErr, download.ErrDestinationBusy):
		item.Status = download.StatusQueued
		m.retryAt[id] = m.opts.Now().Add(busyRetryDelay)
	case canceled:
		// Service shutdown: resume automatically on the next start.
		item.Status = download.StatusQueued
	default:
		item.Status = download.StatusFailed
		item.Error = runErr.Error()
	}
	if err := m.repo.Update(context.Background(), item); err != nil {
		// The row stays "running"; requeueOrphans recovers it on the next tick.
		m.log.Error("record finished download", "id", id, "status", item.Status, "err", err)
		return
	}
	m.log.Info("download stopped", "id", id, "status", item.Status, "completed", item.Completed, "total", item.Total, "error", item.Error)
	m.publish(download.EventUpdated, item)
}

// publishProgress coalesces engine progress reports into one event per
// running download whose byte counts changed.
func (m *Manager) publishProgress() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.active {
		completed, total := j.progress()
		if j.item.Status != download.StatusRunning || (completed == j.item.Completed && total == j.item.Total) {
			continue
		}
		j.item.Completed, j.item.Total = completed, total
		m.publish(download.EventProgress, j.item)
	}
}

// flush persists live progress of running downloads. UpdateProgress is
// conditional on the running status, so it may run outside the lock.
// ponytail: a flush racing Pause→Resume can briefly store older bytes; the
// next flush corrects them.
func (m *Manager) flush(ctx context.Context) {
	type pending struct {
		id               string
		completed, total int64
	}
	m.mu.Lock()
	batch := make([]pending, 0, len(m.active))
	for id, j := range m.active {
		if j.item.Status == download.StatusRunning {
			completed, total := j.progress()
			batch = append(batch, pending{id, completed, total})
		}
	}
	m.mu.Unlock()
	for _, p := range batch {
		if err := m.repo.UpdateProgress(ctx, p.id, p.completed, p.total); err != nil {
			m.log.Error("flush download progress", "id", p.id, "err", err)
		}
	}
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.active {
		j.cancel()
	}
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) publish(eventType download.EventType, item download.Item) {
	m.opts.Publish(download.Event{Type: eventType, Item: item})
}

func validateURL(rawURL string) error {
	if rawURL == "" {
		return errors.New("download URL is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("download URL must be an absolute http or https URL")
	}
	return nil
}

func newID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("create download ID: %w", err)
	}
	return hex.EncodeToString(data[:]), nil
}
