package testutil_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// TestDatabaseConnectionAndSchema is the cross-cutting schema-smoke check:
// after EnsureTestSchema runs, every table the migrations declare must be
// present. It was previously in internal/api/repository/repository_test.go
// but moved here when the repository package split into per-table
// sub-packages — no single sub-package owns "the schema" anymore.
func TestDatabaseConnectionAndSchema(t *testing.T) {
	db := testutil.SetupTestDB(t)

	t.Run("ping database", func(t *testing.T) {
		if err := db.PingContext(t.Context()); err != nil {
			t.Fatalf("ping database: %v", err)
		}
	})

	t.Run("schema", func(t *testing.T) {
		for _, name := range testutil.TableNames {
			t.Run(name, func(t *testing.T) {
				assertTableExists(t, db, name)
			})
		}
	})
}

func assertTableExists(t *testing.T, db *sql.DB, name string) {
	t.Helper()

	const q = "SELECT name FROM sqlite_master WHERE type='table' AND name=?"
	var found string
	err := db.QueryRowContext(t.Context(), q, name).Scan(&found)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		t.Errorf("table %q is missing — migrations likely failed", name)
	case err != nil:
		t.Fatalf("query sqlite_master for %q: %v", name, err)
	}
}
