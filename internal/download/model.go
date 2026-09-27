// Package download defines the service-owned download model shared by the
// manager, store, and API layers.
package download

import (
	"context"
	"time"
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
}

type Runner interface {
	Download(context.Context, string, string, func(Progress)) error
}
