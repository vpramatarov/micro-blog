package categories_test

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/repository/categories"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestCreateAndGetCategory(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	ctx := t.Context()

	id, err := r.Create(ctx, "Engineering")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if id == 0 {
		t.Fatal("zero id returned")
	}

	got, err := r.GetById(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Name != "Engineering" {
		t.Errorf("name: got %q", got.Name)
	}
}

func TestCreateCategoryDuplicate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	ctx := t.Context()

	if _, err := r.Create(ctx, "Design"); err != nil {
		t.Fatalf("first: %v", err)
	}

	_, err := r.Create(ctx, "Design")
	if !errors.Is(err, categories.ErrCategoryDuplicate) {
		t.Errorf("got %v, want ErrCategoryDuplicate", err)
	}
}

func TestGetCategoryNotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	if _, err := r.GetById(t.Context(), 999_999); !errors.Is(err, categories.ErrCategoryNotFound) {
		t.Errorf("got %v, want ErrCategoryNotFound", err)
	}
}

func TestUpdateCategory(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	ctx := t.Context()

	id, _ := r.Create(ctx, "old name")
	if err := r.Update(ctx, id, "new name"); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := r.GetById(ctx, id)
	if got.Name != "new name" {
		t.Errorf("name: got %q, want %q", got.Name, "new name")
	}
}

func TestUpdateCategoryNotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	err := r.Update(t.Context(), 999_999, "x")
	if !errors.Is(err, categories.ErrCategoryNotFound) {
		t.Errorf("got %v, want ErrCategoryNotFound", err)
	}
}

func TestUpdateCategoryDuplicate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	ctx := t.Context()

	_, _ = r.Create(ctx, "alpha")
	id2, _ := r.Create(ctx, "beta")
	err := r.Update(ctx, id2, "alpha")
	if !errors.Is(err, categories.ErrCategoryDuplicate) {
		t.Errorf("got %v, want ErrCategoryDuplicate", err)
	}
}

func TestDeleteCategory(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	ctx := t.Context()

	id, _ := r.Create(ctx, "doomed")
	if err := r.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.GetById(ctx, id); !errors.Is(err, categories.ErrCategoryNotFound) {
		t.Errorf("after delete: got %v, want ErrCategoryNotFound", err)
	}
}

func TestDeleteCategoryNotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	err := r.Delete(t.Context(), 999_999)
	if !errors.Is(err, categories.ErrCategoryNotFound) {
		t.Errorf("got %v, want ErrCategoryNotFound", err)
	}
}

// TestDeleteCategoryInUse pins the RESTRICT behavior. The default
// 'Uncategorized' category (id=1) is referenced by any post we insert with
// no explicit category_id; deleting it must fail with ErrCategoryInUse.
func TestDeleteCategoryInUse(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	ctx := t.Context()

	// Need a user to own the post (author_id FK).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, email, password_hash, role_id) VALUES (1, 'u', 'u@example.com', 'h', 3)`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO posts (author_id, title, markdown_content, html_content, slug) VALUES (1, 't', 'mmmmmmmmmm', '<p>m</p>', 'in-use-post')`); err != nil {
		t.Fatalf("seed post: %v", err)
	}

	err := r.Delete(ctx, 1) // 'Uncategorized', referenced by the post above
	if !errors.Is(err, categories.ErrCategoryInUse) {
		t.Errorf("got %v, want ErrCategoryInUse", err)
	}
}

func TestCategoryExists(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	ctx := t.Context()

	// id=1 is seeded by migration 00006.
	ok, err := r.Exists(ctx, 1)
	if err != nil || !ok {
		t.Errorf("seeded id=1 exists: ok=%v err=%v", ok, err)
	}

	ok, err = r.Exists(ctx, 999_999)
	if err != nil || ok {
		t.Errorf("missing id: ok=%v err=%v", ok, err)
	}
}

func TestListAndCountCategories(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := categories.New(db)
	ctx := t.Context()

	// Seed: 1 row already exists ('Uncategorized') from migration 00006.
	_, _ = r.Create(ctx, "alpha")
	_, _ = r.Create(ctx, "beta")

	got, err := r.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(got) != 3 {
		t.Errorf("len: got %d, want 3", len(got))
	}

	n, err := r.Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}

	if n != 3 {
		t.Errorf("count: got %d, want 3", n)
	}
}
