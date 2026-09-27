// Package store persists service state.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zichuanxu/godl/internal/download"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("download not found")

type SQLite struct {
	db *sql.DB
}

func Open(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLite{db: db}, nil
}

func migrate(db *sql.DB) error {
	const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS downloads (
    id TEXT PRIMARY KEY,
    url TEXT NOT NULL,
    destination TEXT NOT NULL,
    status TEXT NOT NULL,
    completed INTEGER NOT NULL DEFAULT 0,
    total INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS downloads_created_at ON downloads(created_at, id);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate SQLite database: %w", err)
	}
	return nil
}

func (s *SQLite) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close SQLite database: %w", err)
	}
	return nil
}

func (s *SQLite) Create(ctx context.Context, item download.Item) error {
	const query = `INSERT INTO downloads
(id, url, destination, status, completed, total, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, query, item.ID, item.URL, item.Destination, item.Status,
		item.Completed, item.Total, item.Error, item.CreatedAt.Format(time.RFC3339Nano), item.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create download %s: %w", item.ID, err)
	}
	return nil
}

func (s *SQLite) Get(ctx context.Context, id string) (download.Item, error) {
	const query = `SELECT id, url, destination, status, completed, total, error, created_at, updated_at
FROM downloads WHERE id = ?`
	item, err := scanItem(s.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return download.Item{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return download.Item{}, fmt.Errorf("get download %s: %w", id, err)
	}
	return item, nil
}

func (s *SQLite) List(ctx context.Context) ([]download.Item, error) {
	const query = `SELECT id, url, destination, status, completed, total, error, created_at, updated_at
FROM downloads ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list downloads: %w", err)
	}
	defer rows.Close()

	items := make([]download.Item, 0)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scan download: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate downloads: %w", err)
	}
	return items, nil
}

func (s *SQLite) Update(ctx context.Context, item download.Item) error {
	const query = `UPDATE downloads SET url = ?, destination = ?, status = ?, completed = ?, total = ?, error = ?, updated_at = ? WHERE id = ?`
	result, err := s.db.ExecContext(ctx, query, item.URL, item.Destination, item.Status, item.Completed,
		item.Total, item.Error, item.UpdatedAt.Format(time.RFC3339Nano), item.ID)
	if err != nil {
		return fmt.Errorf("update download %s: %w", item.ID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count updated download %s: %w", item.ID, err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, item.ID)
	}
	return nil
}

type scanner interface {
	Scan(...any) error
}

func scanItem(row scanner) (download.Item, error) {
	var item download.Item
	var createdAt, updatedAt string
	if err := row.Scan(&item.ID, &item.URL, &item.Destination, &item.Status, &item.Completed,
		&item.Total, &item.Error, &createdAt, &updatedAt); err != nil {
		return download.Item{}, err
	}
	var err error
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return download.Item{}, fmt.Errorf("parse created_at: %w", err)
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return download.Item{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return item, nil
}
