package main

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Link is a single slug -> target mapping plus its audit/usage metadata.
type Link struct {
	Slug          string
	TargetURL     string
	CreatedAt     string
	UpdatedAt     string
	CreatedBy     string
	Clicks        int64
	LastVisitedAt sql.NullString
}

// Store wraps the SQLite database. SQLite is a single-writer engine; we keep a
// single connection (SetMaxOpenConns(1)) so writes serialize cleanly and we
// never trip "database is locked".
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS links (
	slug            TEXT PRIMARY KEY,
	target_url      TEXT NOT NULL,
	created_at      TEXT,
	updated_at      TEXT,
	created_by      TEXT,
	clicks          INTEGER DEFAULT 0,
	last_visited_at TEXT
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`

// OpenStore opens (creating if needed) the SQLite database, applies WAL and
// busy_timeout pragmas, and ensures the schema exists.
func OpenStore(path string) (*Store, error) {
	// Pragmas in the DSN apply to every connection in the pool.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// GetLink returns the link for slug, or (nil, nil) if it does not exist.
func (s *Store) GetLink(slug string) (*Link, error) {
	row := s.db.QueryRow(
		`SELECT slug, target_url, created_at, updated_at, created_by, clicks, last_visited_at
		 FROM links WHERE slug = ?`, slug)
	var l Link
	err := row.Scan(&l.Slug, &l.TargetURL, &l.CreatedAt, &l.UpdatedAt, &l.CreatedBy, &l.Clicks, &l.LastVisitedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// CreateLink inserts a new slug -> target mapping. It fails if the slug exists.
func (s *Store) CreateLink(slug, target, createdBy string) error {
	now := nowUTC()
	_, err := s.db.Exec(
		`INSERT INTO links (slug, target_url, created_at, updated_at, created_by, clicks)
		 VALUES (?, ?, ?, ?, ?, 0)`,
		slug, target, now, now, createdBy)
	return err
}

// UpdateTarget changes the target URL of an existing slug.
func (s *Store) UpdateTarget(slug, target string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE links SET target_url = ?, updated_at = ? WHERE slug = ?`,
		target, nowUTC(), slug)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteLink removes a slug.
func (s *Store) DeleteLink(slug string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM links WHERE slug = ?`, slug)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// settingRootRedirect is the settings key for the configurable root (/) redirect.
const settingRootRedirect = "root_redirect"

// GetSetting returns the value for key, or "" if it is unset.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// SetSetting upserts key=value. An empty value deletes the row (treated as unset).
func (s *Store) SetSetting(key, value string) error {
	if value == "" {
		_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// RecordVisit increments the click counter and stamps last_visited_at.
func (s *Store) RecordVisit(slug string) error {
	_, err := s.db.Exec(
		`UPDATE links SET clicks = clicks + 1, last_visited_at = ? WHERE slug = ?`,
		nowUTC(), slug)
	return err
}

// ListLinks returns all links ordered by creation time (newest first).
func (s *Store) ListLinks() ([]Link, error) {
	rows, err := s.db.Query(
		`SELECT slug, target_url, created_at, updated_at, created_by, clicks, last_visited_at
		 FROM links ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Slug, &l.TargetURL, &l.CreatedAt, &l.UpdatedAt, &l.CreatedBy, &l.Clicks, &l.LastVisitedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
