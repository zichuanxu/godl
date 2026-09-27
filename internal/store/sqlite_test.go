package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/secrets"
	"github.com/zichuanxu/godl/internal/settings"
	"github.com/zichuanxu/godl/internal/store"
)

func TestSQLiteStorePersistsDownloadLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "godl.db")

	db, err := store.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}

	created := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	item := download.Item{
		ID:          "download-1",
		URL:         "https://example.com/file.bin",
		Destination: "/tmp/file.bin",
		Status:      download.StatusQueued,
		CreatedAt:   created,
		UpdatedAt:   created,
	}
	if err := db.Create(ctx, item); err != nil {
		t.Fatal(err)
	}

	item.Status = download.StatusRunning
	item.Completed = 512
	item.Total = 1024
	item.UpdatedAt = created.Add(time.Second)
	if err := db.Update(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	got, err := db.Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, item) {
		t.Fatalf("persisted item = %+v; want %+v", got, item)
	}

	items, err := db.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !reflect.DeepEqual(items[0], item) {
		t.Fatalf("list = %+v; want [%+v]", items, item)
	}
}

func TestSQLiteStoreReportsMissingDownload(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "godl.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Get(context.Background(), "missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get error = %v; want ErrNotFound", err)
	}
}

func TestSQLiteStoreProgressOnlyUpdatesRunningItemsAndDeletes(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "godl.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	item := download.Item{ID: "d", URL: "https://example.com/f", Destination: "/tmp/f", Status: download.StatusRunning, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateProgress(ctx, "d", 10, 100); err != nil {
		t.Fatal(err)
	}
	item.Status = download.StatusCompleted
	item.Completed, item.Total = 100, 100
	if err := db.Update(ctx, item); err != nil {
		t.Fatal(err)
	}
	// A late flush must not rewind a completed item.
	if err := db.UpdateProgress(ctx, "d", 50, 100); err != nil {
		t.Fatal(err)
	}
	got, err := db.Get(ctx, "d")
	if err != nil {
		t.Fatal(err)
	}
	if got.Completed != 100 || got.Status != download.StatusCompleted {
		t.Fatalf("item after late flush = %+v", got)
	}

	if err := db.Delete(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get(ctx, "d"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after delete error = %v; want ErrNotFound", err)
	}
	if err := db.Delete(ctx, "d"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete error = %v; want ErrNotFound", err)
	}
}

func TestSQLiteStoreRoundTripsOptionsAndSettings(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "godl.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	item := download.Item{
		ID: "x", URL: "https://example.com/a", Destination: "/tmp/a", Status: download.StatusQueued,
		Priority: download.PriorityHigh, Connections: 4, SpeedLimit: 1000, Checksum: "sha256:00",
		Headers: map[string]string{"Cookie": "a=b"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	got, err := db.Get(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Priority != 1 || got.Connections != 4 || got.SpeedLimit != 1000 || got.Checksum != "sha256:00" || got.Headers["Cookie"] != "a=b" {
		t.Fatalf("options not persisted: %+v", got)
	}

	if _, ok, err := db.LoadSettings(ctx); err != nil || ok {
		t.Fatalf("fresh database has settings: ok=%v err=%v", ok, err)
	}
	want := settings.Default()
	want.MaxConcurrent = 7
	want.Sites = []settings.Site{{Host: "example.com", Connections: 2}}
	if err := db.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := db.LoadSettings(ctx)
	if err != nil || !ok || loaded.MaxConcurrent != 7 || len(loaded.Sites) != 1 || loaded.Sites[0].Connections != 2 {
		t.Fatalf("settings round trip: %+v ok=%v err=%v", loaded, ok, err)
	}
}

// A database written by v0.2 (schema version 0) migrates in place.
func TestSQLiteStoreMigratesVersionZeroDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE downloads (id TEXT PRIMARY KEY, url TEXT NOT NULL, destination TEXT NOT NULL,
status TEXT NOT NULL, completed INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0,
error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO downloads VALUES ('old', 'https://example.com/f', '/tmp/f', 'paused', 5, 10, '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	db, err := store.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	item, err := db.Get(context.Background(), "old")
	if err != nil || item.Completed != 5 || item.Priority != 0 || item.Headers != nil {
		t.Fatalf("migrated item = %+v, err %v", item, err)
	}
}

// Headers and site credentials are sealed at rest; with another key they read
// as empty instead of failing.
func TestSQLiteStoreSealsSecrets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "godl.db")
	key := make([]byte, 32)
	sealer, _ := secrets.New(key)
	db, err := store.Open(path, sealer)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := download.Item{ID: "x", URL: "https://example.com/a", Destination: "/tmp/a", Status: download.StatusQueued,
		Headers: map[string]string{"Cookie": "session=secret"}, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	s := settings.Default()
	s.Sites = []settings.Site{{Host: "example.com", Username: "me", Password: "hunter2", Headers: map[string]string{"X-Token": "t"}}}
	if err := db.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Get(ctx, "x"); got.Headers["Cookie"] != "session=secret" {
		t.Fatalf("headers = %v", got.Headers)
	}
	if got, _, _ := db.LoadSettings(ctx); got.Sites[0].Password != "hunter2" || got.Sites[0].Headers["X-Token"] != "t" || got.Sites[0].Username != "me" {
		t.Fatalf("site = %+v", got.Sites[0])
	}
	_ = db.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wal, _ := os.ReadFile(path + "-wal")
	for _, secret := range []string{"session=secret", "hunter2", "X-Token"} {
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(wal, []byte(secret)) {
			t.Fatalf("%q stored in plain text", secret)
		}
	}

	key[0] = 1
	other, _ := secrets.New(key)
	db, err = store.Open(path, other)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got, err := db.Get(ctx, "x"); err != nil || got.Headers != nil {
		t.Fatalf("foreign key: headers = %v, err = %v", got.Headers, err)
	}
	if got, _, err := db.LoadSettings(ctx); err != nil || got.Sites[0].Password != "" || got.Sites[0].Username != "me" {
		t.Fatalf("foreign key: site = %+v, err = %v", got.Sites[0], err)
	}
}
