// Package sqlite implements paste.MetaStore and paste.BlobStore on a single
// SQLite database file. It uses the pure-Go modernc.org/sqlite driver, so the
// binary builds without CGO.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/devesh-anand/pst/internal/paste"

	_ "modernc.org/sqlite"
)

// migrations are applied in order; PRAGMA user_version records how many ran.
// Never edit an existing entry, only append.
var migrations = []string{
	`CREATE TABLE pastes (
		id                TEXT PRIMARY KEY,
		created_at        INTEGER NOT NULL,
		expires_at        INTEGER,
		burn_after_read   INTEGER NOT NULL DEFAULT 0,
		delete_token_hash BLOB NOT NULL,
		size              INTEGER NOT NULL
	) STRICT;
	CREATE INDEX pastes_expires_at ON pastes (expires_at) WHERE expires_at IS NOT NULL;
	CREATE TABLE blobs (
		id   TEXT PRIMARY KEY,
		data BLOB NOT NULL
	) STRICT;`,
}

// DB is an open SQLite database. Meta and Blobs expose it as the two paste
// storage interfaces; they share one file but can be mixed with other backends
// (e.g. SQLite metadata with S3 blobs).
type DB struct {
	db *sql.DB
}

// MetaStore implements paste.MetaStore.
type MetaStore struct{ db *sql.DB }

// BlobStore implements paste.BlobStore.
type BlobStore struct{ db *sql.DB }

var (
	_ paste.MetaStore = (*MetaStore)(nil)
	_ paste.BlobStore = (*BlobStore)(nil)
)

func (d *DB) Meta() *MetaStore  { return &MetaStore{db: d.db} }
func (d *DB) Blobs() *BlobStore { return &BlobStore{db: d.db} }

// Open opens (creating if needed) the database at path and applies migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",   // readers don't block the writer
		"busy_timeout(5000)",  // wait for locks instead of failing with SQLITE_BUSY
		"synchronous(NORMAL)", // safe with WAL, much faster than FULL
		"foreign_keys(ON)",
	} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		// The usual cause in containers is a volume mounted root-owned while
		// pst runs as a non-root user.
		return nil, fmt.Errorf("open sqlite database %s (is its directory writable by this user?): %w", path, err)
	}
	d := &DB{db: db}
	if err := d.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
	}
	// PRAGMA does not accept bound parameters; the value is an int we control.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations))); err != nil {
		return err
	}
	return tx.Commit()
}

const pasteColumns = `id, created_at, expires_at, burn_after_read, delete_token_hash, size`

func (s *MetaStore) Create(ctx context.Context, p *paste.Paste) error {
	var expires sql.NullInt64
	if p.ExpiresAt != nil {
		expires = sql.NullInt64{Int64: p.ExpiresAt.Unix(), Valid: true}
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO pastes (`+pasteColumns+`) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		p.ID, p.CreatedAt.Unix(), expires, p.BurnAfterRead, p.DeleteTokenHash, p.Size)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return paste.ErrExists
	}
	return nil
}

func (s *MetaStore) Get(ctx context.Context, id string) (*paste.Paste, error) {
	return scanPaste(s.db.QueryRowContext(ctx, `SELECT `+pasteColumns+` FROM pastes WHERE id = ?`, id))
}

func (s *MetaStore) Take(ctx context.Context, id string) (*paste.Paste, error) {
	// A single DELETE ... RETURNING is atomic: only one caller gets the row.
	return scanPaste(s.db.QueryRowContext(ctx, `DELETE FROM pastes WHERE id = ? RETURNING `+pasteColumns, id))
}

func (s *MetaStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM pastes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return paste.ErrNotFound
	}
	return nil
}

func (s *MetaStore) DeleteExpired(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM pastes WHERE id IN (
			SELECT id FROM pastes WHERE expires_at IS NOT NULL AND expires_at <= ? LIMIT ?
		) RETURNING id`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func scanPaste(row *sql.Row) (*paste.Paste, error) {
	var (
		p       paste.Paste
		created int64
		expires sql.NullInt64
	)
	err := row.Scan(&p.ID, &created, &expires, &p.BurnAfterRead, &p.DeleteTokenHash, &p.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, paste.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = time.Unix(created, 0).UTC()
	if expires.Valid {
		t := time.Unix(expires.Int64, 0).UTC()
		p.ExpiresAt = &t
	}
	return &p, nil
}

func (s *BlobStore) Put(ctx context.Context, id string, data []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO blobs (id, data) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET data = excluded.data`, id, data)
	return err
}

func (s *BlobStore) Get(ctx context.Context, id string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM blobs WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, paste.ErrNotFound
	}
	return data, err
}

func (s *BlobStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, id)
	return err
}
