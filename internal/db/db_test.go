package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/evanstern/coda/internal/db/migrations"
)

func latestVersion(t *testing.T) int {
	t.Helper()
	v, err := migrations.Latest()
	if err != nil {
		t.Fatalf("migrations.Latest: %v", err)
	}
	return v
}

func TestOpen_CreatesSchemaIdempotently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "coda.db")

	d1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	d1.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("second open (idempotent): %v", err)
	}
	defer d2.Close()

	for _, table := range []string{"orchestrators", "features", "hook_events"} {
		var name string
		err := d2.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
			table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("table %s not found: %v", table, err)
		}
	}
}

func TestOpen_EnablesForeignKeys(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "coda.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	var fk int
	if err := d.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("pragma foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys pragma = %d, want 1", fk)
	}
}

func TestDefaultHome_CodaHomeWins(t *testing.T) {
	t.Setenv("CODA_HOME", "/tmp/custom-coda")
	h, err := DefaultHome()
	if err != nil {
		t.Fatal(err)
	}
	if h != "/tmp/custom-coda" {
		t.Fatalf("DefaultHome = %q, want /tmp/custom-coda", h)
	}
}

func TestDefaultHome_XDGFallback(t *testing.T) {
	t.Setenv("CODA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	h, err := DefaultHome()
	if err != nil {
		t.Fatal(err)
	}
	if h != "/tmp/xdg-state/coda" {
		t.Fatalf("DefaultHome = %q, want /tmp/xdg-state/coda", h)
	}
}

func TestSchemaVersionIsRecordedOnFreshDB(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "coda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	want := latestVersion(t)
	var v int
	if err := d.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if v != want {
		t.Fatalf("schema_version = %d, want %d", v, want)
	}
}

func TestOpen_FreshDBAppliesAllMigrations(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "coda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	want := latestVersion(t)
	var v int
	if err := d.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if v != want {
		t.Fatalf("schema_version = %d, want %d (all migrations applied)", v, want)
	}
}

func TestOpen_StaleDBMigratesForward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coda.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d2.Close()

	want := latestVersion(t)
	var v int
	if err := d2.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if v != want {
		t.Fatalf("schema_version after reopen = %d, want %d", v, want)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coda.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	future := latestVersion(t) + 1
	if _, err := d.Exec(
		`INSERT INTO schema_version (version, applied_at) VALUES (?, unixepoch())`,
		future,
	); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	d.Close()

	_, err = Open(path)
	if err == nil {
		t.Fatal("expected error opening DB with newer schema, got nil")
	}
	if got := err.Error(); !contains(got, "newer than this coda-core") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpen_MigrationTransactionRollback(t *testing.T) {
	base, err := migrations.All()
	if err != nil {
		t.Fatalf("migrations.All: %v", err)
	}
	latest := base[len(base)-1].Version

	bad := make([]Migration, 0, len(base)+1)
	bad = append(bad, base...)
	bad = append(bad, Migration{
		Version: latest + 1,
		Name:    "intentional-failure",
		SQL: `CREATE TABLE rollback_probe (id INTEGER PRIMARY KEY);
INSERT INTO schema_version (version, applied_at) VALUES (` +
			strconv.Itoa(latest+1) + `, unixepoch());
SELECT syntax error;`,
	})

	path := filepath.Join(t.TempDir(), "coda.db")
	_, err = openWithMigrations(path, bad)
	if err == nil {
		t.Fatal("expected migration failure, got nil")
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after failed migration: %v", err)
	}
	defer d.Close()

	var v int
	if err := d.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if v != latest {
		t.Fatalf("schema_version = %d, want %d (failed migration must not bump)", v, latest)
	}

	var name string
	err = d.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='rollback_probe'`,
	).Scan(&name)
	if err == nil {
		t.Fatal("rollback_probe table exists; migration transaction did not roll back")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows for missing rollback_probe row, got: %v", err)
	}
}

func TestCheckConstraintRejectsBogusOrchestratorState(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "coda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	_, err = d.Exec(
		`INSERT INTO orchestrators (name, config_dir, state, created_at, updated_at)
		 VALUES ('x', '/tmp/x', 'bogus', 0, 0)`,
	)
	if err == nil {
		t.Fatal("expected CHECK constraint to reject 'bogus' state")
	}
	if !contains(err.Error(), "CHECK constraint") && !contains(err.Error(), "constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckConstraintRejectsBogusFeatureState(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "coda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if _, err := d.Exec(
		`INSERT INTO orchestrators (name, config_dir, state, created_at, updated_at)
		 VALUES ('o', '/tmp/o', 'stopped', 0, 0)`,
	); err != nil {
		t.Fatal(err)
	}
	_, err = d.Exec(
		`INSERT INTO features (name, orchestrator_id, project, branch, worktree_dir, state, created_at, updated_at)
		 VALUES ('f', 1, 'p', 'b', '/tmp/w', 'bogus', 0, 0)`,
	)
	if err == nil {
		t.Fatal("expected CHECK constraint to reject 'bogus' state on features")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
