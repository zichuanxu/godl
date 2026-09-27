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
