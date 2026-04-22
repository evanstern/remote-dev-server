// Package db provides SQLite access for the coda-core v2 lifecycle state
// store. It embeds the schema and applies it idempotently on open.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// SchemaVersion is the schema revision this coda-core binary understands.
// Bumped in lockstep with breaking changes to schema.sql.
const SchemaVersion = 1

// DefaultHome returns the CODA_HOME directory, defaulting to
// $XDG_STATE_HOME/coda (or ~/.local/state/coda).
func DefaultHome() (string, error) {
	if h := os.Getenv("CODA_HOME"); h != "" {
		return h, nil
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "coda"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "coda"), nil
}

// DefaultPath returns the default path to coda.db inside CODA_HOME.
func DefaultPath() (string, error) {
	h, err := DefaultHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "coda.db"), nil
}

// Open opens the database at the given path, creating the parent dir
// with 0700 if it does not exist. It enables WAL and foreign keys and
// applies the embedded schema idempotently.
//
// If path is empty, DefaultPath() is used.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		path = p
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	// Pragmas are passed via DSN so they're applied per-connection.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Single connection avoids WAL concurrency surprises in a CLI tool.
	d.SetMaxOpenConns(1)

	if _, err := d.Exec(schemaSQL); err != nil {
		d.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	var dbVer int
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&dbVer); err != nil {
		d.Close()
		return nil, fmt.Errorf("read schema_version: %w", err)
	}
	if dbVer > SchemaVersion {
		d.Close()
		return nil, fmt.Errorf(
			"coda.db schema version %d is newer than this coda-core (supports up to %d). Upgrade coda-core or use an older DB.",
			dbVer, SchemaVersion,
		)
	}

	return d, nil
}
