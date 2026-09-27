// Package api exposes the loopback service contract.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zichuanxu/godl/internal/download"
)

type Downloads interface {
	Add(context.Context, string, string) (download.Item, error)
	List(context.Context) ([]download.Item, error)
	Pause(context.Context, string) error
}

type Broker struct {
	next atomic.Int64

	mu          sync.Mutex
	bufferSize  int
	subscribers map[chan download.Event]struct{}
}

func NewBroker(bufferSize int) *Broker {
	if bufferSize < 1 {
		bufferSize = 1
	}
	return &Broker{bufferSize: bufferSize, subscribers: make(map[chan download.Event]struct{})}
}

func (b *Broker) Publish(event download.Event) {
	event.Sequence = b.next.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	for subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func (b *Broker) subscribe() (<-chan download.Event, func()) {
	ch := make(chan download.Event, b.bufferSize)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subscribers, ch)
		b.mu.Unlock()
	}
}

type Handler struct {
	downloads Downloads
	broker    *Broker
}

func NewHandler(downloads Downloads, broker *Broker) http.Handler {
	if broker == nil {
		broker = NewBroker(64)
	}
	h := &Handler{downloads: downloads, broker: broker}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping", h.ping)
	mux.HandleFunc("GET /v1/downloads", h.list)
	mux.HandleFunc("POST /v1/downloads", h.add)
	mux.HandleFunc("POST /v1/downloads/{action}", h.pause)
	mux.HandleFunc("GET /v1/events", h.events)
	return mux
}

func (h *Handler) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.downloads.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Unable to list downloads")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) add(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL         string `json:"url"`
		Destination string `json:"destination"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Request body must contain a URL and destination")
		return
	}
	item, err := h.downloads.Add(r.Context(), strings.TrimSpace(input.URL), strings.TrimSpace(input.Destination))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_DOWNLOAD", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (h *Handler) pause(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	id, ok := strings.CutSuffix(action, ":pause")
	if !ok || id == "" {
		http.NotFound(w, r)
		return
	}
	if err := h.downloads.Pause(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Download not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STREAM_UNAVAILABLE", "Streaming is unavailable")
		return
	}
	events, unsubscribe := h.broker.subscribe()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-events:
			data, err := json.Marshal(event.Item)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
