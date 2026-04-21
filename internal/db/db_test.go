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
