package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zichuanxu/nimget/internal/api"
	"github.com/zichuanxu/nimget/internal/download"
	"github.com/zichuanxu/nimget/internal/settings"
)

const token = "test-token"

type fakeDownloads struct {
	mu       sync.Mutex
	items    []download.Item
	calls    []string
	failure  error
	settings settings.Settings
}

func (f *fakeDownloads) Add(_ context.Context, req download.Request) (download.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item := download.Item{ID: "id-1", URL: req.URL, Destination: req.Destination, Status: download.StatusQueued, Priority: req.Priority}
	f.items = append(f.items, item)
	return item, nil
}

func (f *fakeDownloads) List(context.Context) ([]download.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]download.Item(nil), f.items...), nil
}

func (f *fakeDownloads) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.failure
}

func (f *fakeDownloads) Pause(_ context.Context, id string) error  { return f.record("pause " + id) }
func (f *fakeDownloads) Resume(_ context.Context, id string) error { return f.record("resume " + id) }
func (f *fakeDownloads) Retry(_ context.Context, id string) error  { return f.record("retry " + id) }
func (f *fakeDownloads) Delete(_ context.Context, id string, files bool) error {
	return f.record(fmt.Sprintf("delete %s files=%v", id, files))
}
func (f *fakeDownloads) Update(_ context.Context, id string, p download.Patch) error {
	return f.record(fmt.Sprintf("update %s priority=%d", id, *p.Priority))
}
func (f *fakeDownloads) Settings() settings.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings
}
func (f *fakeDownloads) UpdateSettings(_ context.Context, s settings.Settings) error {
	if err := f.record("settings"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = s
	return nil
}

func newServer(t *testing.T, downloads api.Downloads, broker *api.Broker) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(api.NewHandler(downloads, broker, api.Config{Token: token}))
	t.Cleanup(server.Close)
	return server
}

// send issues an authorized JSON command unless headers override it.
func send(t *testing.T, method, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		if name == "Host" {
			req.Host = value
			continue
		}
		if value == "" {
			req.Header.Del(name)
			continue
		}
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestDownloadHTTPContract(t *testing.T) {
	downloads := &fakeDownloads{}
	server := newServer(t, downloads, api.NewBroker(16))

	resp := send(t, http.MethodPost, server.URL+"/v1/downloads", `{"url":"https://example.com/file.bin","destination":"/tmp/file.bin","priority":1,"headers":{"Cookie":"a=b"}}`, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d; want 202", resp.StatusCode)
	}
	if resp := send(t, http.MethodPost, server.URL+"/v1/downloads", `{"url":"x","bogus":1}`, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d; want 400", resp.StatusCode)
	}
	var created download.Item
	decodeJSON(t, resp.Body, &created)
	if created.ID != "id-1" || created.Status != download.StatusQueued || created.Priority != 1 {
		t.Fatalf("created = %+v", created)
	}

	resp = send(t, http.MethodGet, server.URL+"/v1/downloads", "", nil)
	var listed []download.Item
	decodeJSON(t, resp.Body, &listed)
	if resp.StatusCode != http.StatusOK || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("GET = %d %+v", resp.StatusCode, listed)
	}

	for _, verb := range []string{"pause", "resume", "retry"} {
		if resp := send(t, http.MethodPost, server.URL+"/v1/downloads/id-1:"+verb, "", nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s status = %d; want 204", verb, resp.StatusCode)
		}
	}
	if resp := send(t, http.MethodPatch, server.URL+"/v1/downloads/id-1", `{"priority":-1}`, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("patch status = %d; want 204", resp.StatusCode)
	}
	if resp := send(t, http.MethodDelete, server.URL+"/v1/downloads/id-1?files=true", "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d; want 204", resp.StatusCode)
	}
	resp = send(t, http.MethodPut, server.URL+"/v1/settings", `{"maxConcurrent":5,"connections":8,"hostConnections":16}`, nil)
	var saved settings.Settings
	decodeJSON(t, resp.Body, &saved)
	if resp.StatusCode != http.StatusOK || saved.MaxConcurrent != 5 {
		t.Fatalf("PUT settings = %d %+v", resp.StatusCode, saved)
	}
	resp = send(t, http.MethodGet, server.URL+"/v1/settings", "", nil)
	decodeJSON(t, resp.Body, &saved)
	if saved.MaxConcurrent != 5 {
		t.Fatalf("GET settings = %+v", saved)
	}
	if resp := send(t, http.MethodPost, server.URL+"/v1/downloads/id-1:explode", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown verb status = %d; want 404", resp.StatusCode)
	}
	want := []string{"pause id-1", "resume id-1", "retry id-1", "update id-1 priority=-1", "delete id-1 files=true", "settings"}
	if fmt.Sprint(downloads.calls) != fmt.Sprint(want) {
		t.Fatalf("calls = %v; want %v", downloads.calls, want)
	}
}

func TestCommandErrorsMapToStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: x", download.ErrNotFound), http.StatusNotFound},
		{fmt.Errorf("%w: cannot pause", download.ErrInvalidTransition), http.StatusConflict},
		{download.ErrActive, http.StatusConflict},
		{fmt.Errorf("disk full"), http.StatusInternalServerError},
	} {
		server := newServer(t, &fakeDownloads{failure: tc.err}, nil)
		if resp := send(t, http.MethodPost, server.URL+"/v1/downloads/x:pause", "", nil); resp.StatusCode != tc.want {
			t.Errorf("%v: status = %d; want %d", tc.err, resp.StatusCode, tc.want)
		}
	}
}

func TestLocalAuthorizationDenials(t *testing.T) {
	server := newServer(t, &fakeDownloads{}, nil)
	list := server.URL + "/v1/downloads"
	for name, tc := range map[string]struct {
		method, url string
		headers     map[string]string
		want        int
	}{
		"missing token":       {http.MethodGet, list, map[string]string{"Authorization": ""}, http.StatusUnauthorized},
		"wrong token":         {http.MethodGet, list, map[string]string{"Authorization": "Bearer nope"}, http.StatusUnauthorized},
		"basic auth":          {http.MethodGet, list, map[string]string{"Authorization": "Basic " + token}, http.StatusUnauthorized},
		"query token on list": {http.MethodGet, list + "?token=" + token, map[string]string{"Authorization": ""}, http.StatusUnauthorized},
		"DNS rebinding host":  {http.MethodGet, list, map[string]string{"Host": "evil.example:51000"}, http.StatusForbidden},
		"browser origin":      {http.MethodGet, list, map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		"simple text/plain":   {http.MethodPost, list, map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		"no content type":     {http.MethodPost, server.URL + "/v1/downloads/x:pause", map[string]string{"Content-Type": ""}, http.StatusUnsupportedMediaType},
		"ping without token":  {http.MethodGet, server.URL + "/v1/ping", map[string]string{"Authorization": ""}, http.StatusOK},
		"localhost host":      {http.MethodGet, list, map[string]string{"Host": "localhost:51000"}, http.StatusOK},
	} {
		if resp := send(t, tc.method, tc.url, `{}`, tc.headers); resp.StatusCode != tc.want {
			t.Errorf("%s: status = %d; want %d", name, resp.StatusCode, tc.want)
		}
	}

	open := httptest.NewServer(api.NewHandler(&fakeDownloads{}, nil, api.Config{}))
	defer open.Close()
	req, _ := http.NewRequest(http.MethodGet, open.URL+"/v1/downloads", nil)
	req.Header.Set("Authorization", "Bearer ")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("empty configured token status = %d; want 401", resp.StatusCode)
	}
}

func TestSSEStreamsSnapshotThenDeltas(t *testing.T) {
	downloads := &fakeDownloads{items: []download.Item{{ID: "id-1", Status: download.StatusRunning, Completed: 1, Total: 10}}}
	broker := api.NewBroker(16)
	server := newServer(t, downloads, broker)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events?token="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events response = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(resp.Body)

	snapshot := readEvent(t, reader)
	if !strings.Contains(snapshot, "event: snapshot\n") || !strings.Contains(snapshot, `"id":"id-1"`) {
		t.Fatalf("first event is not the snapshot:\n%s", snapshot)
	}

	broker.Publish(download.Event{Type: download.EventProgress, Item: download.Item{ID: "id-1", Completed: 5, Total: 10}})
	delta := readEvent(t, reader)
	if !strings.Contains(delta, "id: 1\n") || !strings.Contains(delta, "event: progress\n") || !strings.Contains(delta, `"completed":5`) {
		t.Fatalf("unexpected delta:\n%s", delta)
	}
}

func readEvent(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var event strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			return event.String()
		}
		event.WriteString(line)
	}
}

func decodeJSON(t *testing.T, r io.Reader, dst any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(dst); err != nil {
		t.Fatal(err)
	}
}
