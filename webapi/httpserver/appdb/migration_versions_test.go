package appdb

import (
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every migration must claim a version number no other migration claims.
//
// This exists because the collision is invisible until it is expensive.
// Two branches each add "the next" migration, each is green on its own
// base, git reports no conflict because they are different files -- and
// the moment both are on main, goose panics on OPEN. Not on a query, not
// on the migration in question: every server using the application
// database refuses to start, and every test that opens one dies with it.
//
// That is what happened with 0045, twice claimed by an unrelated pair of
// PRs merged twenty minutes apart. The failure surfaced as an unrelated
// root-only integration test, because that was simply the first job to
// open a database.
//
// A unit test is the right place for this: it costs nothing, it fails on
// the branch that would cause it rather than after the merge, and it
// names the two files instead of leaving a panic to be read.
func TestMigrationVersionsAreUnique(t *testing.T) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		t.Fatalf("reading the embedded migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no migrations are embedded; this test would pass vacuously")
	}

	seen := map[int64]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		// goose parses the version from the leading digits, so that is
		// what has to be unique -- not the whole filename, which
		// obviously differs or they could not both exist.
		base := strings.TrimSuffix(path.Base(name), ".sql")
		digits := base
		if i := strings.IndexByte(base, '_'); i >= 0 {
			digits = base[:i]
		}
		version, perr := strconv.ParseInt(digits, 10, 64)
		if perr != nil {
			t.Errorf("%s does not begin with a version number goose can read: %v", name, perr)
			continue
		}
		if other, dup := seen[version]; dup {
			t.Errorf("migrations %s and %s both claim version %d; "+
				"goose panics on OPEN when two do, so nothing that uses the "+
				"application database will start", other, name, version)
			continue
		}
		seen[version] = name
	}
}

// And the embedded set must actually migrate, which is the other half:
// unique versions that do not apply are no better than duplicates.
func TestEmbeddedMigrationsApply(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("applying the embedded migrations: %v", err)
	}
	// Twice, because goose records what it applied and a second call has
	// to be a no-op rather than a re-run.
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("re-applying the embedded migrations: %v", err)
	}
}
