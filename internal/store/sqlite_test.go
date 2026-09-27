package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/store"
)

func TestSQLiteStorePersistsDownloadLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "godl.db")

	db, err := store.Open(path)
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

	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	got, err := db.Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != item {
		t.Fatalf("persisted item = %+v; want %+v", got, item)
	}

	items, err := db.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0] != item {
		t.Fatalf("list = %+v; want [%+v]", items, item)
	}
}

func TestSQLiteStoreReportsMissingDownload(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "godl.db"))
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
	db, err := store.Open(filepath.Join(t.TempDir(), "godl.db"))
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
