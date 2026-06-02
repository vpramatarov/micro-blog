package slug_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/slug"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

// TestMain runs once per binary. The Generate-side tests in slug_test.go don't touch the DB;
// the Finder tests below do, so we set up the test schema unconditionally — idempotent thanks to testutil's sync.Once gate.
func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// TestFinderGenerate_Posts exercises the SQL contract once against the
// posts table; the categories/tags variants below piggy-back on the same implementation so they only need a smoke test each.
func TestFinderGenerate_Posts(t *testing.T) {
	db := testutil.SetupTestDB(t)
	f := slug.NewFinder(db, slug.TablePosts)
	ctx := t.Context()

	// Seed: posts table needs an author. Insert a user so the FK on the
	// posts inserts below doesn't trip.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, email, password_hash, role_id) VALUES (1, 'u', 'u@example.com', 'h', 3)`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Empty table: base is free.
	got, err := f.Generate(ctx, "hello-world", 0)
	if err != nil || got != "hello-world" {
		t.Fatalf("empty table: got %q (%v), want hello-world", got, err)
	}

	// Seed the base; expect "-2".
	if _, err := db.ExecContext(ctx,
		`INSERT INTO posts (id, author_id, title, markdown_content, html_content, slug, status) VALUES (1, 1, 't', 'mmmmmmmmmm', '<p>m</p>', 'hello-world', 'published')`); err != nil {
		t.Fatalf("seed post: %v", err)
	}

	got, err = f.Generate(ctx, "hello-world", 0)
	if err != nil || got != "hello-world-2" {
		t.Fatalf("after seed: got %q (%v), want hello-world-2", got, err)
	}

	// Seed "-2"; expect "-3".
	if _, err := db.ExecContext(ctx,
		`INSERT INTO posts (id, author_id, title, markdown_content, html_content, slug, status) VALUES (2, 1, 't', 'mmmmmmmmmm', '<p>m</p>', 'hello-world-2', 'published')`); err != nil {
		t.Fatalf("seed -2: %v", err)
	}

	got, err = f.Generate(ctx, "hello-world", 0)
	if err != nil || got != "hello-world-3" {
		t.Fatalf("after -2 seed: got %q (%v), want hello-world-3", got, err)
	}

	// excludeID=1 means row id=1 ('hello-world') is invisible to the
	// scanner — base becomes free again. This is the update path.
	got, err = f.Generate(ctx, "hello-world", 1)
	if err != nil || got != "hello-world" {
		t.Fatalf("excludeID: got %q (%v), want hello-world (own row excluded)", got, err)
	}
}

// TestFinderGenerate_Categories smoke-tests the categories table.
// SetupTestDB re-seeds id=1 'Uncategorized' between tests; that's the only pre-existing row.
func TestFinderGenerate_Categories(t *testing.T) {
	db := testutil.SetupTestDB(t)
	f := slug.NewFinder(db, slug.TableCategories)
	ctx := t.Context()
	got, err := f.Generate(ctx, "eng", 0)
	if err != nil || got != "eng" {
		t.Fatalf("fresh: got %q (%v), want eng", got, err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO categories (name, slug) VALUES ('Eng', 'eng')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err = f.Generate(ctx, "eng", 0)
	if err != nil || got != "eng-2" {
		t.Fatalf("after seed: got %q (%v), want eng-2", got, err)
	}
}

// TestFinderGenerate_Tags — same smoke test for the tags table.
func TestFinderGenerate_Tags(t *testing.T) {
	db := testutil.SetupTestDB(t)
	f := slug.NewFinder(db, slug.TableTags)
	ctx := t.Context()
	got, err := f.Generate(ctx, "go", 0)
	if err != nil || got != "go" {
		t.Fatalf("fresh: got %q (%v), want go", got, err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO tags (name, slug) VALUES ('Go', 'go')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err = f.Generate(ctx, "go", 0)
	if err != nil || got != "go-2" {
		t.Fatalf("after seed: got %q (%v), want go-2", got, err)
	}
}
