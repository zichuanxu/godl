// Package store persists service state.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/secrets"
	"github.com/zichuanxu/godl/internal/settings"
	_ "modernc.org/sqlite"
)

var ErrNotFound = download.ErrNotFound

type SQLite struct {
	db     *sql.DB
	sealer *secrets.Sealer
}

// Open opens the database, sealing request headers and site credentials with
// sealer; a nil sealer stores them in plain text (tests only).
func Open(path string, sealer *secrets.Sealer) (*SQLite, error) {
	// Pragmas in the DSN apply to every connection the pool opens. WAL with
	// synchronous=NORMAL stays durable across application crashes; only an OS
	// crash can lose the last transactions, which a resume re-downloads.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLite{db: db, sealer: sealer}, nil
}

// migrations run in order; PRAGMA user_version records how many have run.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS downloads (
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
CREATE INDEX IF NOT EXISTS downloads_created_at ON downloads(created_at, id);`,
	`ALTER TABLE downloads ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN connections INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN speed_limit INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN checksum TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN headers TEXT NOT NULL DEFAULT '';
CREATE TABLE settings (id INTEGER PRIMARY KEY CHECK (id = 1), value TEXT NOT NULL);`,
	`ALTER TABLE settings ADD COLUMN secrets TEXT NOT NULL DEFAULT '';`,
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this godl (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("migrate SQLite database: %w", err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate SQLite database to version %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record schema version %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migrate SQLite database to version %d: %w", i+1, err)
		}
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
	headers, err := s.encodeHeaders(item.Headers)
	if err != nil {
		return err
	}
	const query = `INSERT INTO downloads (id, url, destination, status, completed, total, error, priority, connections, speed_limit, checksum, headers, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, query, item.ID, item.URL, item.Destination, item.Status,
		item.Completed, item.Total, item.Error, item.Priority, item.Connections, item.SpeedLimit, item.Checksum, headers,
		item.CreatedAt.Format(time.RFC3339Nano), item.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create download %s: %w", item.ID, err)
	}
	return nil
}

func (s *SQLite) Get(ctx context.Context, id string) (download.Item, error) {
	const query = `SELECT id, url, destination, status, completed, total, error, priority, connections, speed_limit, checksum, headers, created_at, updated_at
FROM downloads WHERE id = ?`
	item, err := s.scanItem(s.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return download.Item{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return download.Item{}, fmt.Errorf("get download %s: %w", id, err)
	}
	return item, nil
}

func (s *SQLite) List(ctx context.Context) ([]download.Item, error) {
	const query = `SELECT id, url, destination, status, completed, total, error, priority, connections, speed_limit, checksum, headers, created_at, updated_at
FROM downloads ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list downloads: %w", err)
	}
	defer rows.Close()

	items := make([]download.Item, 0)
	for rows.Next() {
		item, err := s.scanItem(rows)
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

// Update stores everything but the request headers, which change only
// through UpdateHeaders: rewriting them on every transition would replace
// sealed values this process cannot open with empty ones.
func (s *SQLite) Update(ctx context.Context, item download.Item) error {
	const query = `UPDATE downloads SET url = ?, destination = ?, status = ?, completed = ?, total = ?, error = ?,
priority = ?, connections = ?, speed_limit = ?, checksum = ?, updated_at = ? WHERE id = ?`
	result, err := s.db.ExecContext(ctx, query, item.URL, item.Destination, item.Status, item.Completed,
		item.Total, item.Error, item.Priority, item.Connections, item.SpeedLimit, item.Checksum,
		item.UpdatedAt.Format(time.RFC3339Nano), item.ID)
	if err != nil {
		return fmt.Errorf("update download %s: %w", item.ID, err)
	}
	return affected(result, item.ID)
}

// UpdateHeaders replaces a download's request headers.
func (s *SQLite) UpdateHeaders(ctx context.Context, id string, h map[string]string) error {
	headers, err := s.encodeHeaders(h)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE downloads SET headers = ? WHERE id = ?`, headers, id)
	if err != nil {
		return fmt.Errorf("update download headers %s: %w", id, err)
	}
	return affected(result, id)
}

func affected(result sql.Result, id string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count updated download %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil
}

func (s *SQLite) UpdateProgress(ctx context.Context, id string, completed, total int64) error {
	const query = `UPDATE downloads SET completed = ?, total = ? WHERE id = ? AND status = ?`
	if _, err := s.db.ExecContext(ctx, query, completed, total, id, download.StatusRunning); err != nil {
		return fmt.Errorf("update download progress %s: %w", id, err)
	}
	return nil
}

func (s *SQLite) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM downloads WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete download %s: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted download %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil
}

type scanner interface {
	Scan(...any) error
}

func (s *SQLite) scanItem(row scanner) (download.Item, error) {
	var item download.Item
	var headers, createdAt, updatedAt string
	if err := row.Scan(&item.ID, &item.URL, &item.Destination, &item.Status, &item.Completed,
		&item.Total, &item.Error, &item.Priority, &item.Connections, &item.SpeedLimit, &item.Checksum, &headers,
		&createdAt, &updatedAt); err != nil {
		return download.Item{}, err
	}
	var err error
	if item.Headers, err = s.decodeHeaders(headers); err != nil {
		return download.Item{}, err
	}
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

func (s *SQLite) seal(plain string) (string, error) {
	if s.sealer == nil {
		return plain, nil
	}
	return s.sealer.Seal(plain)
}

// open reverses seal. A value sealed under a key that is gone, as after a
// restart without an OS keyring, reads as empty: the download then asks for
// its credentials again (DESIGN.md 7).
func (s *SQLite) open(value string) (string, error) {
	if s.sealer == nil || value == "" {
		return value, nil
	}
	plain, err := s.sealer.Open(value)
	if errors.Is(err, secrets.ErrUnreadable) {
		return "", nil
	}
	return plain, err
}

// encodeHeaders stores request headers as sealed JSON.
func (s *SQLite) encodeHeaders(h map[string]string) (string, error) {
	if len(h) == 0 {
		return "", nil
	}
	data, err := json.Marshal(h)
	if err != nil {
		return "", err
	}
	return s.seal(string(data))
}

func (s *SQLite) decodeHeaders(value string) (map[string]string, error) {
	plain, err := s.open(value)
	if err != nil || plain == "" {
		return nil, err
	}
	var h map[string]string
	if err := json.Unmarshal([]byte(plain), &h); err != nil {
		return nil, fmt.Errorf("parse headers: %w", err)
	}
	return h, nil
}

// sealedSettings is the sealed part of the settings: site headers and
// passwords by site index, and the proxy URL, which may carry credentials.
type sealedSettings struct {
	Sites    []siteSecret `json:"sites"`
	ProxyURL string       `json:"proxyURL,omitempty"`
}

type siteSecret struct {
	Headers  map[string]string `json:"headers,omitempty"`
	Password string            `json:"password,omitempty"`
}

func (s *SQLite) LoadSettings(ctx context.Context) (settings.Settings, bool, error) {
	var value, sealed string
	err := s.db.QueryRowContext(ctx, `SELECT value, secrets FROM settings WHERE id = 1`).Scan(&value, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return settings.Settings{}, false, nil
	}
	if err != nil {
		return settings.Settings{}, false, fmt.Errorf("load settings: %w", err)
	}
	// Start from the defaults so fields added later get sensible values.
	out := settings.Default()
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return settings.Settings{}, false, fmt.Errorf("parse settings: %w", err)
	}
	plain, err := s.open(sealed)
	if err != nil {
		return settings.Settings{}, false, err
	}
	if plain != "" {
		var secret sealedSettings
		if strings.HasPrefix(plain, "[") { // v0.3 stored the site list alone
			err = json.Unmarshal([]byte(plain), &secret.Sites)
		} else {
			err = json.Unmarshal([]byte(plain), &secret)
		}
		if err != nil {
			return settings.Settings{}, false, fmt.Errorf("parse sealed settings: %w", err)
		}
		for i := range out.Sites {
			if i < len(secret.Sites) {
				out.Sites[i].Headers, out.Sites[i].Password = secret.Sites[i].Headers, secret.Sites[i].Password
			}
		}
		if secret.ProxyURL != "" {
			out.Proxy.URL = secret.ProxyURL
		}
	}
	return out, true, nil
}

// SaveSettings stores the settings with site headers, site passwords, and
// the proxy URL sealed apart from the readable document. Sealed settings
// this process cannot open, because the keyring was unavailable, are kept
// rather than replaced.
func (s *SQLite) SaveSettings(ctx context.Context, value settings.Settings) error {
	value.Sites = append([]settings.Site(nil), value.Sites...)
	secret := sealedSettings{Sites: make([]siteSecret, len(value.Sites)), ProxyURL: value.Proxy.URL}
	for i := range value.Sites {
		secret.Sites[i] = siteSecret{Headers: value.Sites[i].Headers, Password: value.Sites[i].Password}
		value.Sites[i].Headers, value.Sites[i].Password = nil, ""
	}
	value.Proxy.URL = ""
	doc, err := json.Marshal(value)
	if err != nil {
		return err
	}
	secretJSON, err := json.Marshal(secret)
	if err != nil {
		return err
	}
	sealed, err := s.seal(string(secretJSON))
	if err != nil {
		return err
	}
	var stored string
	err = s.db.QueryRowContext(ctx, `SELECT secrets FROM settings WHERE id = 1`).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load settings: %w", err)
	}
	if !s.readable(stored) {
		sealed = stored
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO settings (id, value, secrets) VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET value = excluded.value, secrets = excluded.secrets`, string(doc), sealed); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}

// readable reports whether a stored value can be opened with this key.
func (s *SQLite) readable(value string) bool {
	if s.sealer == nil || value == "" {
		return true
	}
	_, err := s.sealer.Open(value)
	return !errors.Is(err, secrets.ErrUnreadable)
}
