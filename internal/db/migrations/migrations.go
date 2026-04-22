// Package migrations holds the forward-only, numbered SQL migrations
// applied by db.Open() to bring a coda.db up to the current schema.
//
// Migrations are named `NNN_<slug>.sql`, where NNN is a three-digit
// zero-padded integer starting at 001 and incrementing contiguously.
// Each file is applied in its own transaction and is responsible for
// its own `INSERT INTO schema_version (version, applied_at) ...` row.
//
// Forward-only: there are no downgrade files. Rollback of a partially
// applied migration happens at the transaction level in db.Open().
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"sync"
)

//go:embed *.sql
var embeddedFS embed.FS

// Migration is a single numbered SQL file loaded from the migrations
// directory.
type Migration struct {
	Version int
	Name    string // slug portion of the filename, for logs
	SQL     string
}

var (
	loadOnce sync.Once
	loaded   []Migration
	loadErr  error
)

// filenameRE matches `NNN_<slug>.sql` where NNN is three digits and
// slug is lowercase alphanumeric with dashes.
var filenameRE = regexp.MustCompile(`^(\d{3})_([a-z0-9-]+)\.sql$`)

// All returns migrations embedded in the binary, sorted by Version
// ascending. It errors if filenames are malformed, if there are
// duplicate versions, or if the numbering has gaps.
//
// The result is cached: the first successful call loads and validates
// the embedded FS once; subsequent calls return the same slice.
func All() ([]Migration, error) {
	loadOnce.Do(func() {
		loaded, loadErr = load(embeddedFS)
	})
	return loaded, loadErr
}

// Latest returns the highest Version across All(). It returns 0 and
// an error if the migration set is empty or fails to load.
func Latest() (int, error) {
	ms, err := All()
	if err != nil {
		return 0, err
	}
	if len(ms) == 0 {
		return 0, fmt.Errorf("no migrations found")
	}
	return ms[len(ms)-1].Version, nil
}

// load reads and validates every `*.sql` entry at the root of fsys.
// It is exposed unexported so tests can pass a fstest.MapFS to exercise
// the negative-path validations (malformed names, gaps, duplicates).
func load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var migs []Migration
	seen := make(map[int]string)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		m := filenameRE.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf("migrations: malformed filename %q (want NNN_<slug>.sql)", name)
		}
		version, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("migrations: parse version in %q: %w", name, err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations: duplicate version %d (%q and %q)", version, prev, name)
		}
		seen[version] = name

		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("migrations: read %q: %w", name, err)
		}
		migs = append(migs, Migration{
			Version: version,
			Name:    m[2],
			SQL:     string(b),
		})
	}

	sort.Slice(migs, func(i, j int) bool {
		return migs[i].Version < migs[j].Version
	})

	// Numbering must start at 1 and have no gaps.
	for i, mi := range migs {
		want := i + 1
		if mi.Version != want {
			return nil, fmt.Errorf("migrations: gap at version %d (found %d)", want, mi.Version)
		}
	}

	return migs, nil
}
