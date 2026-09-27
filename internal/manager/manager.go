// Package manager owns the ordered service download queue.
package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zichuanxu/godl/internal/download"
)

type Options struct {
	MaxConcurrent int
	Publish       func(download.Event)
	Now           func() time.Time
}

type Manager struct {
	repo   download.Repository
	runner download.Runner
	opts   Options

	wake chan struct{}

	mu     sync.Mutex
	active map[string]context.CancelFunc
	paused map[string]bool
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
	return &Manager{
		repo: repo, runner: runner, opts: opts,
		wake: make(chan struct{}, 1), active: make(map[string]context.CancelFunc), paused: make(map[string]bool),
	}, nil
}

func (m *Manager) Add(ctx context.Context, rawURL, destination string) (download.Item, error) {
	if rawURL == "" {
		return download.Item{}, errors.New("download URL is required")
	}
	if destination == "" {
		return download.Item{}, errors.New("destination is required")
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
	if err := m.repo.Create(ctx, item); err != nil {
		return download.Item{}, err
	}
	m.publish(download.EventUpdated, item)
	m.signal()
	return item, nil
}

func (m *Manager) List(ctx context.Context) ([]download.Item, error) {
	return m.repo.List(ctx)
}

func (m *Manager) Pause(ctx context.Context, id string) error {
	item, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.paused[id] = true
	cancel := m.active[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	item.Status = download.StatusPaused
	item.Error = ""
	item.UpdatedAt = m.opts.Now().UTC()
	if err := m.repo.Update(ctx, item); err != nil {
		m.mu.Lock()
		delete(m.paused, id)
		m.mu.Unlock()
		return err
	}
	m.publish(download.EventUpdated, item)
	return nil
}

func (m *Manager) Run(ctx context.Context) {
	m.signal()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-m.wake:
			m.schedule(ctx)
		}
	}
}

func (m *Manager) schedule(ctx context.Context) {
	for {
		m.mu.Lock()
		capacity := m.opts.MaxConcurrent - len(m.active)
		m.mu.Unlock()
		if capacity <= 0 {
			return
		}
		items, err := m.repo.List(ctx)
		if err != nil {
			return
		}
		var next *download.Item
		for i := range items {
			if items[i].Status == download.StatusQueued {
				next = &items[i]
				break
			}
		}
		if next == nil {
			return
		}
		m.start(ctx, *next)
	}
}

func (m *Manager) start(parent context.Context, item download.Item) {
	workCtx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	if _, exists := m.active[item.ID]; exists {
		m.mu.Unlock()
		cancel()
		return
	}
	delete(m.paused, item.ID)
	m.active[item.ID] = cancel
	m.mu.Unlock()

	item.Status = download.StatusRunning
	item.Error = ""
	item.UpdatedAt = m.opts.Now().UTC()
	if err := m.repo.Update(parent, item); err != nil {
		m.finishActive(item.ID)
		return
	}
	m.publish(download.EventUpdated, item)

	go func() {
		err := m.runner.Download(workCtx, item.URL, item.Destination, func(progress download.Progress) {
			m.mu.Lock()
			paused := m.paused[item.ID]
			m.mu.Unlock()
			if paused {
				return
			}
			item.Completed = progress.Completed
			item.Total = progress.Total
			item.UpdatedAt = m.opts.Now().UTC()
			if updateErr := m.repo.Update(context.Background(), item); updateErr == nil {
				m.publish(download.EventProgress, item)
			}
		})

		m.finishActive(item.ID)
		stored, getErr := m.repo.Get(context.Background(), item.ID)
		if getErr == nil && stored.Status == download.StatusPaused {
			m.mu.Lock()
			delete(m.paused, item.ID)
			m.mu.Unlock()
			m.signal()
			return
		}
		item.UpdatedAt = m.opts.Now().UTC()
		if err == nil {
			item.Status = download.StatusCompleted
			item.Completed = item.Total
			item.Error = ""
		} else if errors.Is(err, context.Canceled) {
			item.Status = download.StatusPaused
			item.Error = ""
		} else {
			item.Status = download.StatusFailed
			item.Error = err.Error()
		}
		if updateErr := m.repo.Update(context.Background(), item); updateErr == nil {
			m.publish(download.EventUpdated, item)
		}
		m.signal()
	}()
}

func (m *Manager) finishActive(id string) {
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cancel := range m.active {
		cancel()
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

func newID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("create download ID: %w", err)
	}
	return hex.EncodeToString(data[:]), nil
}
