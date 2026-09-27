package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/api"
	"github.com/zichuanxu/godl/internal/download"
)

type fakeDownloads struct {
	mu     sync.Mutex
	items  []download.Item
	paused string
}

func (f *fakeDownloads) Add(_ context.Context, rawURL, destination string) (download.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item := download.Item{ID: "id-1", URL: rawURL, Destination: destination, Status: download.StatusQueued}
	f.items = append(f.items, item)
	return item, nil
}

func (f *fakeDownloads) List(context.Context) ([]download.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]download.Item(nil), f.items...), nil
}

func (f *fakeDownloads) Pause(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = id
	return nil
}

func TestDownloadHTTPContract(t *testing.T) {
	downloads := &fakeDownloads{}
	broker := api.NewBroker(16)
	server := httptest.NewServer(api.NewHandler(downloads, broker))
	defer server.Close()

	body := strings.NewReader(`{"url":"https://example.com/file.bin","destination":"/tmp/file.bin"}`)
	resp, err := http.Post(server.URL+"/v1/downloads", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d; want 202", resp.StatusCode)
	}
	var created download.Item
	decodeJSON(t, resp.Body, &created)
	_ = resp.Body.Close()
	if created.ID != "id-1" || created.Status != download.StatusQueued {
		t.Fatalf("created = %+v", created)
	}

	resp, err = http.Get(server.URL + "/v1/downloads")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d; want 200", resp.StatusCode)
	}
	var listed []download.Item
	decodeJSON(t, resp.Body, &listed)
	_ = resp.Body.Close()
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed = %+v", listed)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/downloads/id-1:pause", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || downloads.paused != "id-1" {
		t.Fatalf("pause status = %d, id = %q", resp.StatusCode, downloads.paused)
	}
}

func TestSSEPublishesProgressWithEventID(t *testing.T) {
	downloads := &fakeDownloads{}
	broker := api.NewBroker(16)
	server := httptest.NewServer(api.NewHandler(downloads, broker))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}

	broker.Publish(download.Event{Type: download.EventProgress, Item: download.Item{ID: "id-1", Completed: 5, Total: 10}})

	reader := bufio.NewReader(resp.Body)
	var event bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			break
		}
		event.WriteString(line)
	}
	text := event.String()
	if !strings.Contains(text, "id: 1\n") || !strings.Contains(text, "event: progress\n") || !strings.Contains(text, `"completed":5`) {
		t.Fatalf("unexpected SSE event:\n%s", text)
	}
}

func decodeJSON(t *testing.T, r io.Reader, dst any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(dst); err != nil {
		t.Fatal(err)
	}
}
