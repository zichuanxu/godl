// Package download defines the service-owned download model shared by the
// manager, store, and API layers.
package download

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("download not found")
	// ErrInvalidTransition reports a command that the item's status forbids,
	// such as pausing a completed download.
	ErrInvalidTransition = errors.New("invalid download state transition")
	// ErrActive reports a command that requires the runner to have stopped.
	ErrActive = errors.New("download is active")
	// ErrInvalidDownload reports a rejected URL or destination.
	ErrInvalidDownload = errors.New("invalid download")
	// ErrDestinationBusy reports that another process holds the destination
	// lock; the download is retried later instead of failing.
	ErrDestinationBusy = errors.New("destination is locked by another download")
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

type Item struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Destination string    `json:"destination"`
	Status      Status    `json:"status"`
	Completed   int64     `json:"completed"`
	Total       int64     `json:"total"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Progress struct {
	Completed int64
	Total     int64
}

type EventType string

const (
	EventUpdated  EventType = "updated"
	EventProgress EventType = "progress"
	EventDeleted  EventType = "deleted"
)

type Event struct {
	Sequence int64     `json:"sequence,omitempty"`
	Type     EventType `json:"type"`
	Item     Item      `json:"item"`
}

type Repository interface {
	Create(context.Context, Item) error
	Get(context.Context, string) (Item, error)
	List(context.Context) ([]Item, error)
	Update(context.Context, Item) error
	// UpdateProgress stores byte progress only while the item is still running,
	// so a periodic flush can never overwrite a newer terminal status.
	UpdateProgress(ctx context.Context, id string, completed, total int64) error
	Delete(context.Context, string) error
}

type Runner interface {
	Download(context.Context, string, string, func(Progress)) error
	// Discard removes the resumable partial state kept for a destination.
	Discard(destination string) error
}
