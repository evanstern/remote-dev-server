package migrations

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestAll_ParsesAndOrders(t *testing.T) {
	ms, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no migrations loaded")
	}
	for i, m := range ms {
		if m.Version != i+1 {
			t.Fatalf("migrations[%d].Version = %d, want %d", i, m.Version, i+1)
		}
	}
	if ms[0].Version != 1 {
		t.Fatalf("first migration version = %d, want 1", ms[0].Version)
	}
	if ms[0].Name != "init" {
		t.Fatalf("first migration name = %q, want %q", ms[0].Name, "init")
	}
}

func TestLatest_MatchesLastMigration(t *testing.T) {
	ms, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	latest, err := Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest != ms[len(ms)-1].Version {
		t.Fatalf("Latest = %d, want %d", latest, ms[len(ms)-1].Version)
	}
}

func TestLoad_RejectsMalformedFilename(t *testing.T) {
	cases := []string{
		"abc.sql",
		"1_init.sql",
		"001_BAD_CAPS.sql",
		"001_has space.sql",
		"001-init.sql",
	}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			fs := fstest.MapFS{
				bad: &fstest.MapFile{Data: []byte("SELECT 1;")},
			}
			_, err := load(fs)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", bad)
			}
			if !strings.Contains(err.Error(), "malformed filename") {
				t.Fatalf("expected malformed filename error, got: %v", err)
			}
		})
	}
}

func TestLoad_RejectsGap(t *testing.T) {
	fs := fstest.MapFS{
		"001_a.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"003_c.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	}
	_, err := load(fs)
	if err == nil {
		t.Fatal("expected gap error, got nil")
	}
	if !strings.Contains(err.Error(), "gap at version 2") {
		t.Fatalf("expected gap error, got: %v", err)
	}
}

func TestLoad_RejectsDuplicate(t *testing.T) {
	fs := fstest.MapFS{
		"001_a.sql":     &fstest.MapFile{Data: []byte("SELECT 1;")},
		"001_other.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
	}
	_, err := load(fs)
	if err == nil {
		t.Fatal("expected duplicate error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate version 1") {
		t.Fatalf("expected duplicate error, got: %v", err)
	}
}

func TestLoad_RejectsMissingVersionOne(t *testing.T) {
	fs := fstest.MapFS{
		"002_b.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	}
	_, err := load(fs)
	if err == nil {
		t.Fatal("expected error for missing version 1, got nil")
	}
	if !strings.Contains(err.Error(), "gap at version 1") {
		t.Fatalf("expected gap at 1 error, got: %v", err)
	}
}

func TestLoad_HappyPath(t *testing.T) {
	fs := fstest.MapFS{
		"002_b.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
		"001_a.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"003_c.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
	}
	ms, err := load(fs)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(ms) != 3 {
		t.Fatalf("got %d migrations, want 3", len(ms))
	}
	for i, want := range []string{"a", "b", "c"} {
		if ms[i].Version != i+1 || ms[i].Name != want {
			t.Fatalf("ms[%d] = {%d, %q}, want {%d, %q}", i, ms[i].Version, ms[i].Name, i+1, want)
		}
	}
}
