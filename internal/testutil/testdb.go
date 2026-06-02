package testutil

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/vpramatarov/micro-blog/cmd"
	_ "modernc.org/sqlite"
)

// TableNames lists every table managed by the migrations, ordered so that
// children appear before their parents. SetupTestDB deletes rows in this order
// between tests, and schema tests can iterate it to assert each table exists.
var TableNames = []string{
	"post_tags",
	"short_links",
	"posts",
	"refresh_tokens",
	"revoked_jtis",
	"role_permissions",
	"users",
	"roles",
	"permissions",
	"categories",
	"tags",
}

// wipeTableNames is the subset of TableNames that wipeTables actually clears between tests.
// roles, permissions and role_permissions are seeded by migration 00004 and treated as reference data —
// leaving them in place keeps the test DB consistent with production after the migrations DownTo 0 -> Up cycle.
var wipeTableNames = []string{
	"jobs",
	"post_tags",
	"short_links",
	"posts",
	"categories",
	"tags",
	"revoked_jtis",
	"refresh_tokens",
	"users",
}

// findRepoRoot walks up from the current working directory looking for go.mod.
// Reliable regardless of where the test binary was compiled, and works from any package inside the module.
func findRepoRoot() (string, error) {
	start, err := os.Getwd()
	if err != nil {
		return "", err
	}

	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found walking up from %s", start)
		}

		dir = parent
	}
}

// testDBPath returns the absolute path of the shared test SQLite file.
// All test binaries point at the same file; run tests with `go test -p 1 ./...`
// to serialize them — SQLite does not tolerate concurrent writers.
func testDBPath() (string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return "", err
	}

	return filepath.Join(root, "vault_test.db"), nil
}

// testDBDSN wraps the path in a URI carrying the same pragmas as production:
// foreign_keys for cascade enforcement, journal_mode=WAL so concurrent readers
// don't block, and busy_timeout so a contending writer waits rather than
// erroring with SQLITE_BUSY when -p 1 fails to fully serialize.
func testDBDSN() (string, error) {
	path, err := testDBPath()
	if err != nil {
		return "", err
	}

	return "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", nil
}

var (
	migrateOnce sync.Once
	migrateErr  error
)

// EnsureTestSchema brings the shared test DB to the latest schema. Idempotent
// across calls within the same test binary, and safe to call from TestMain (no *testing.T required).
func EnsureTestSchema() error {
	migrateOnce.Do(func() {
		migrateErr = migrateTestDB()
	})

	return migrateErr
}

func migrateTestDB() error {
	dsn, err := testDBDSN()
	if err != nil {
		return fmt.Errorf("resolve test DB path: %w", err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open %s: %w", dsn, err)
	}

	defer db.Close()

	goose.SetBaseFS(cmd.EmbedMigrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}

	if err := goose.DownTo(db, "migrate/migrations", 0); err != nil {
		return fmt.Errorf("goose down-to 0: %w", err)
	}

	if err := goose.Up(db, "migrate/migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	return nil
}

// SetupTestDB ensures the schema is current, wipes all rows, and returns a fresh connection.
// The connection is closed automatically at test cleanup.
func SetupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	if err := EnsureTestSchema(); err != nil {
		t.Fatalf("prepare test schema: %v", err)
	}

	dsn, err := testDBDSN()
	if err != nil {
		t.Fatalf("resolve test DB path: %v", err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := wipeTables(db); err != nil {
		t.Fatalf("wipe test database: %v", err)
	}

	return db
}

func wipeTables(db *sql.DB) error {
	for _, name := range wipeTableNames {
		if _, err := db.Exec("DELETE FROM " + name); err != nil {
			return fmt.Errorf("delete from %s: %w", name, err)
		}

		if _, err := db.Exec("UPDATE sqlite_sequence SET seq=1 WHERE name='" + name + "'"); err != nil {
			return fmt.Errorf("reset autoincrement key for %s table: %w", name, err)
		}

		if _, err := db.Exec(`INSERT OR IGNORE INTO categories (id, name, slug) VALUES (1, 'Uncategorized', 'uncategorized')`); err != nil {
			return fmt.Errorf("reseed categories: %w", err)
		}
	}

	return nil
}
