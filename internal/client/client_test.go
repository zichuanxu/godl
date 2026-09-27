package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zichuanxu/godl/internal/client"
	"github.com/zichuanxu/godl/internal/download"
)

func TestClientListsAddsAndPausesDownloads(t *testing.T) {
	var paused string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/downloads":
			_ = json.NewEncoder(w).Encode([]download.Item{{ID: "existing", Status: download.StatusQueued}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/downloads":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(download.Item{ID: "created", Status: download.StatusQueued})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/downloads/created:pause":
			paused = "created"
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/downloads/created" && r.URL.Query().Get("files") == "true":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	c := client.New(server.URL, "secret", nil)
	items, err := c.List(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != "existing" {
		t.Fatalf("List = %+v, %v", items, err)
	}
	item, err := c.Add(context.Background(), download.Request{URL: "https://example.com/file", Destination: "/tmp/file"})
	if err != nil || item.ID != "created" {
		t.Fatalf("Add = %+v, %v", item, err)
	}
	if err := c.Pause(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	if paused != item.ID {
		t.Fatalf("paused = %q; want %q", paused, item.ID)
	}
	if err := c.Delete(context.Background(), item.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := client.New(server.URL, "wrong", nil).List(context.Background()); err == nil {
		t.Fatal("List with a wrong token succeeded")
	}
}
