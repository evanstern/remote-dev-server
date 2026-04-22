package db

import (
	"path/filepath"
	"testing"
)

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

	var v int
	if err := d.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if v != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", v, SchemaVersion)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coda.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(
		`INSERT INTO schema_version (version, applied_at) VALUES (?, unixepoch())`,
		SchemaVersion+1,
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
