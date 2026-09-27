// Package api exposes the loopback service contract.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zichuanxu/nimget/internal/download"
	"github.com/zichuanxu/nimget/internal/settings"
)

type Downloads interface {
	Add(context.Context, download.Request) (download.Item, error)
	List(context.Context) ([]download.Item, error)
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	Retry(context.Context, string) error
	Update(context.Context, string, download.Patch) error
	Delete(ctx context.Context, id string, removeFiles bool) error
	Settings() settings.Settings
	UpdateSettings(context.Context, settings.Settings) error
}

// Broker fans events out to SSE subscribers. A subscriber that falls behind is
// disconnected rather than silently missing events; it reconnects and receives
// a fresh snapshot.
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

// Publish never blocks, so callers may hold locks while publishing.
func (b *Broker) Publish(event download.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	event.Sequence = b.next.Add(1)
	for subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
			delete(b.subscribers, subscriber)
			close(subscriber)
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

func (b *Broker) sequence() int64 {
	return b.next.Load()
}

type Config struct {
	// Token authorizes every endpoint except /v1/ping. An empty token rejects
	// all authorized requests.
	Token  string
	Logger *slog.Logger
}

type Handler struct {
	downloads Downloads
	broker    *Broker
	token     string
	log       *slog.Logger
}

func NewHandler(downloads Downloads, broker *Broker, cfg Config) http.Handler {
	if broker == nil {
		broker = NewBroker(64)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	h := &Handler{downloads: downloads, broker: broker, token: cfg.Token, log: cfg.Logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping", h.ping)
	mux.HandleFunc("GET /v1/downloads", h.list)
	mux.HandleFunc("POST /v1/downloads", h.add)
	mux.HandleFunc("POST /v1/downloads/{action}", h.command)
	mux.HandleFunc("PATCH /v1/downloads/{id}", h.update)
	mux.HandleFunc("DELETE /v1/downloads/{id}", h.delete)
	mux.HandleFunc("GET /v1/settings", h.getSettings)
	mux.HandleFunc("PUT /v1/settings", h.putSettings)
	mux.HandleFunc("GET /v1/events", h.events)
	return h.guard(mux)
}

// guard enforces the local authorization rules of DESIGN.md section 5.1.
func (h *Handler) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		// Rejecting other names defeats DNS rebinding, where a hostile domain
		// resolves to 127.0.0.1 and the browser sends Host: evil.example.
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			writeError(w, http.StatusForbidden, "FORBIDDEN_HOST", "Host is not a loopback name")
			return
		}
		// Browsers attach Origin to cross-origin requests; no browser origin is
		// trusted until extension pairing exists.
		if r.Header.Get("Origin") != "" {
			writeError(w, http.StatusForbidden, "FORBIDDEN_ORIGIN", "Browser origins are not allowed")
			return
		}
		if r.URL.Path != "/v1/ping" && !h.authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "A valid service token is required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Commands require Content-Type: application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) authorized(r *http.Request) bool {
	if h.token == "" {
		return false
	}
	presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	// EventSource cannot send headers, so the event stream also accepts the
	// token as a query parameter. It is never logged.
	if !ok && r.URL.Path == "/v1/events" {
		presented, ok = r.URL.Query().Get("token"), true
	}
	return ok && subtle.ConstantTimeCompare([]byte(presented), []byte(h.token)) == 1
}

func (h *Handler) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.downloads.List(r.Context())
	if err != nil {
		h.writeCommandError(w, "list downloads", err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// decode reads a JSON body strictly, answering 400 itself on failure.
func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body: "+err.Error())
		return false
	}
	return true
}

func (h *Handler) add(w http.ResponseWriter, r *http.Request) {
	var input download.Request
	if !decode(w, r, &input) {
		return
	}
	item, err := h.downloads.Add(r.Context(), input)
	if err != nil {
		h.writeCommandError(w, "add download", err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var patch download.Patch
	if !decode(w, r, &patch) {
		return
	}
	if err := h.downloads.Update(r.Context(), r.PathValue("id"), patch); err != nil {
		h.writeCommandError(w, "update download", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.downloads.Settings().Masked())
}

func (h *Handler) putSettings(w http.ResponseWriter, r *http.Request) {
	var s settings.Settings
	if !decode(w, r, &s) {
		return
	}
	if err := h.downloads.UpdateSettings(r.Context(), s); err != nil {
		h.writeCommandError(w, "update settings", err)
		return
	}
	writeJSON(w, http.StatusOK, h.downloads.Settings().Masked())
}

func (h *Handler) command(w http.ResponseWriter, r *http.Request) {
	id, verb, ok := strings.Cut(r.PathValue("action"), ":")
	if !ok || id == "" {
		http.NotFound(w, r)
		return
	}
	commands := map[string]func(context.Context, string) error{
		"pause":  h.downloads.Pause,
		"resume": h.downloads.Resume,
		"retry":  h.downloads.Retry,
	}
	run, ok := commands[verb]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := run(r.Context(), id); err != nil {
		h.writeCommandError(w, verb+" download", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	removeFiles := r.URL.Query().Get("files") == "true"
	if err := h.downloads.Delete(r.Context(), r.PathValue("id"), removeFiles); err != nil {
		h.writeCommandError(w, "delete download", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// events streams one snapshot of every download, then deltas. Deltas already
// reflected in the snapshot are skipped by sequence number.
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STREAM_UNAVAILABLE", "Streaming is unavailable")
		return
	}
	events, unsubscribe := h.broker.subscribe()
	defer unsubscribe()
	// Publishers emit an event only after the state it describes is visible
	// to List, so every event numbered at or below the marker is already in
	// the snapshot.
	marker := h.broker.sequence()
	items, err := h.downloads.List(r.Context())
	if err != nil {
		h.writeCommandError(w, "snapshot downloads", err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if err := writeEvent(w, marker, "snapshot", items); err != nil {
		return
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return // fell behind; the client reconnects for a new snapshot
			}
			if event.Sequence <= marker {
				continue
			}
			if err := writeEvent(w, event.Sequence, string(event.Type), event.Item); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, id int64, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, name, data)
	return err
}

func (h *Handler) writeCommandError(w http.ResponseWriter, action string, err error) {
	switch {
	case errors.Is(err, download.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Download not found")
	case errors.Is(err, download.ErrInvalidTransition), errors.Is(err, download.ErrActive):
		writeError(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, download.ErrInvalidDownload):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_DOWNLOAD", err.Error())
	case errors.Is(err, download.ErrInvalidSettings):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_SETTINGS", err.Error())
	default:
		h.log.Error(action, "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Unable to "+action)
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
