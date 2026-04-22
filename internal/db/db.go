// Package db provides SQLite access for the coda-core v2 lifecycle state
// store. On Open, it applies numbered forward-only migrations from
// internal/db/migrations to bring the database up to the current schema.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/evanstern/coda/internal/db/migrations"
	_ "modernc.org/sqlite"
)

// Migration is re-exported from the migrations subpackage so tests and
// callers of openWithMigrations don't need to import it separately.
type Migration = migrations.Migration

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
// applies any pending migrations to bring the DB up to the current
// schema version.
//
// If path is empty, DefaultPath() is used.
func Open(path string) (*sql.DB, error) {
	migs, err := migrations.All()
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	return openWithMigrations(path, migs)
}

// openWithMigrations is the testable seam behind Open: it accepts an
// explicit migration slice so tests can inject a synthetic migration
// (e.g., one whose SQL is intentionally invalid) to exercise the
// transaction rollback behavior of the migration runner.
func openWithMigrations(path string, migs []Migration) (*sql.DB, error) {
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

	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	d.SetMaxOpenConns(1)

	dbVer, err := currentSchemaVersion(d)
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("read schema_version: %w", err)
	}

	if len(migs) == 0 {
		d.Close()
		return nil, fmt.Errorf("no database migrations available")
	}
	latest := migs[len(migs)-1].Version

	if dbVer > latest {
		d.Close()
		return nil, fmt.Errorf(
			"coda.db schema version %d is newer than this coda-core (supports up to %d). Upgrade coda-core or use an older DB.",
			dbVer, latest,
		)
	}

	if dbVer == latest {
		return d, nil
	}

	for _, m := range migs {
		if m.Version <= dbVer {
			continue
		}
		if err := applyMigration(d, m); err != nil {
			d.Close()
			return nil, fmt.Errorf("apply migration %03d_%s: %w", m.Version, m.Name, err)
		}
	}

	return d, nil
}

// currentSchemaVersion returns MAX(version) from the schema_version
// table, or 0 if the table does not yet exist (fresh DB). The existence
// pre-check via sqlite_master avoids relying on sqlite driver error
// string matching.
func currentSchemaVersion(d *sql.DB) (int, error) {
	var name string
	err := d.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='schema_version'`,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	var v int
	if err := d.QueryRow(
		`SELECT COALESCE(MAX(version), 0) FROM schema_version`,
	).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// applyMigration runs one migration inside a transaction following the
// canonical SQLite "Making Other Kinds Of Table Schema Changes" pattern
// (https://sqlite.org/lang_altertable.html §7):
//
//  1. PRAGMA foreign_keys=OFF outside any transaction
//  2. BEGIN
//  3. apply the migration SQL
//  4. PRAGMA foreign_key_check inside the transaction, before commit
//  5. if the check reports any rows, return error -> deferred rollback
//     fires and the schema mutation is discarded
//  6. COMMIT
//  7. PRAGMA foreign_keys=ON outside any transaction
//
// Running the FK check inside the transaction is load-bearing: a check
// after COMMIT would leave an FK-violating schema committed on disk.
func applyMigration(d *sql.DB, m Migration) error {
	if _, err := d.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	defer func() { _, _ = d.Exec(`PRAGMA foreign_keys=ON`) }()

	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec(m.SQL); err != nil {
		return err
	}

	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	if rows.Next() {
		var table, rowid, parent, fkid any
		_ = rows.Scan(&table, &rowid, &parent, &fkid)
		rows.Close()
		return fmt.Errorf(
			"migration %03d_%s violated FK (table=%v parent=%v)",
			m.Version, m.Name, table, parent,
		)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
