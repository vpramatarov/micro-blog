package tags_test

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/repository/tags"
	"github.com/vpramatarov/micro-blog/internal/slug"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestCreateAndGetTag(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tags.New(db)
	ctx := t.Context()

	id, err := r.Create(ctx, "go", "go")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := r.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Name != "go" {
		t.Errorf("name: got %q", got.Name)
	}
}

func TestCreateTagDuplicate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tags.New(db)
	ctx := t.Context()

	if _, err := r.Create(ctx, "go", "go"); err != nil {
		t.Fatalf("first: %v", err)
	}

	_, err := r.Create(ctx, "go", "go-2")
	if !errors.Is(err, tags.ErrTagDuplicate) {
		t.Errorf("got %v, want ErrTagDuplicate", err)
	}
}

func TestUpdateTagDuplicate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tags.New(db)
	ctx := t.Context()

	_, _ = r.Create(ctx, "alpha", "alpha")
	id, _ := r.Create(ctx, "beta", "beta")
	err := r.Update(ctx, id, "alpha", "alpha")
	if !errors.Is(err, tags.ErrTagDuplicate) {
		t.Errorf("got %v, want ErrTagDuplicate", err)
	}
}

func TestGetTagBySlug(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tags.New(db)
	ctx := t.Context()
	id, _ := r.Create(ctx, "Go", "go")
	got, err := r.GetBySlug(ctx, "go")
	if err != nil {
		t.Fatalf("get by slug: %v", err)
	}

	if got.ID != id || got.Name != "Go" || got.Slug != "go" {
		t.Errorf("got %+v", got)
	}

	if _, err := r.GetBySlug(ctx, "missing"); !errors.Is(err, tags.ErrTagNotFound) {
		t.Errorf("miss: got %v, want ErrTagNotFound", err)
	}
}

func TestDeleteTagAlsoCascadesPostTags(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tags.New(db)
	ctx := t.Context()

	// Seed user → post → tag → post_tag join row.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, email, password_hash, role_id) VALUES (1, 'u', 'u@example.com', 'h', 3)`); err != nil {
		t.Fatalf("user: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO posts (id, author_id, title, markdown_content, html_content, slug) VALUES (1, 1, 't', 'mmmmmmmmmm', '<p>m</p>', 'cascade-slug')`); err != nil {
		t.Fatalf("post: %v", err)
	}

	tagID, err := r.Create(ctx, "to-delete", "to-delete")
	if err != nil {
		t.Fatalf("tag: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO post_tags (post_id, tag_id) VALUES (1, ?)`, tagID); err != nil {
		t.Fatalf("post_tag: %v", err)
	}

	if err := r.Delete(ctx, tagID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// post_tags row should be gone via cascade.
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM post_tags WHERE tag_id = ?`, tagID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}

	if n != 0 {
		t.Errorf("post_tags after cascade: got %d, want 0", n)
	}
}

func TestMissingTagIDs(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tags.New(db)
	ctx := t.Context()

	a, _ := r.Create(ctx, "a", "tag-a")
	b, _ := r.Create(ctx, "b", "tag-b")

	missing, err := r.MissingIDs(ctx, []int64{a, b, 999_999})
	if err != nil {
		t.Fatalf("missing: %v", err)
	}

	if !reflect.DeepEqual(missing, []int64{999_999}) {
		t.Errorf("got %v, want [999_999]", missing)
	}

	missing, err = r.MissingIDs(ctx, []int64{a, b})
	if err != nil {
		t.Fatalf("none missing: %v", err)
	}

	if len(missing) != 0 {
		t.Errorf("expected empty, got %v", missing)
	}

	missing, _ = r.MissingIDs(ctx, nil)
	if missing != nil {
		t.Errorf("nil input: got %v, want nil", missing)
	}
}

func TestReplaceTagsForPost(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tags.New(db)
	ctx := t.Context()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, email, password_hash, role_id) VALUES (1, 'u', 'u@example.com', 'h', 3)`); err != nil {
		t.Fatalf("user: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO posts (id, author_id, title, markdown_content, html_content, slug) VALUES (1, 1, 't', 'mmmmmmmmmm', '<p>m</p>', 'replace-slug')`); err != nil {
		t.Fatalf("post: %v", err)
	}

	a, _ := r.Create(ctx, "a", "tag-a")
	b, _ := r.Create(ctx, "b", "tag-b")
	c, _ := r.Create(ctx, "c", "tag-c")

	// Initial set.
	if err := r.ReplaceForPost(ctx, 1, []int64{a, b}); err != nil {
		t.Fatalf("first replace: %v", err)
	}

	got, _ := r.ListForPost(ctx, 1)
	if !sameTagIDs(got, []int64{a, b}) {
		t.Errorf("first: got %v, want %v", tagIDs(got), []int64{a, b})
	}

	// Rewrite.
	if err := r.ReplaceForPost(ctx, 1, []int64{b, c, c}); err != nil { // dedup b duplicate→just c, but bug-safety: dup c
		t.Fatalf("second replace: %v", err)
	}

	got, _ = r.ListForPost(ctx, 1)
	if !sameTagIDs(got, []int64{b, c}) {
		t.Errorf("second: got %v, want %v", tagIDs(got), []int64{b, c})
	}

	// Clear.
	if err := r.ReplaceForPost(ctx, 1, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}

	got, _ = r.ListForPost(ctx, 1)
	if len(got) != 0 {
		t.Errorf("clear: got %v, want empty", got)
	}
}

func TestListTagsForPosts(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tags.New(db)
	ctx := t.Context()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, email, password_hash, role_id) VALUES (1, 'u', 'u@example.com', 'h', 3)`); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO posts (id, author_id, title, markdown_content, html_content, slug) VALUES
		 (1, 1, 't1', 'mmmmmmmmmm', '<p>m</p>', 'list-tags-slug-1'),
		 (2, 1, 't2', 'mmmmmmmmmm', '<p>m</p>', 'list-tags-slug-2'),
		 (3, 1, 't3', 'mmmmmmmmmm', '<p>m</p>', 'list-tags-slug-3')`); err != nil {
		t.Fatalf("posts: %v", err)
	}
	a, _ := r.Create(ctx, "a", "tag-a")
	b, _ := r.Create(ctx, "b", "tag-b")
	_ = r.ReplaceForPost(ctx, 1, []int64{a, b})
	_ = r.ReplaceForPost(ctx, 2, []int64{a})
	// post 3 stays empty

	got, err := r.ListForPosts(ctx, []int64{1, 2, 3})
	if err != nil {
		t.Fatalf("list for posts: %v", err)
	}

	if !sameTagIDs(got[1], []int64{a, b}) {
		t.Errorf("post 1: got %v", tagIDs(got[1]))
	}

	if !sameTagIDs(got[2], []int64{a}) {
		t.Errorf("post 2: got %v", tagIDs(got[2]))
	}

	if len(got[3]) != 0 {
		t.Errorf("post 3: got %v, want empty", got[3])
	}
}

func TestCreateTagSlugDuplicate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tags.New(db)
	ctx := t.Context()
	if _, err := r.Create(ctx, "Go", "go"); err != nil {
		t.Fatalf("first: %v", err)
	}

	_, err := r.Create(ctx, "Go Reloaded", "go")
	if !errors.Is(err, slug.ErrDuplicate) {
		t.Errorf("got %v, want slug.ErrDuplicate", err)
	}
}

func TestFindAvailableSlugTag(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tags.New(db)
	ctx := t.Context()
	got, err := r.GenerateSlug(ctx, "fresh", 0)
	if err != nil || got != "fresh" {
		t.Errorf("free base: got %q (%v), want fresh", got, err)
	}

	if _, err := r.Create(ctx, "Go", "go"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, _ = r.GenerateSlug(ctx, "go", 0)
	if got != "go-2" {
		t.Errorf("first collision: got %q, want go-2", got)
	}
}

func tagIDs(ts []tags.Tag) []int64 {
	out := make([]int64, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}

	return out
}

func sameTagIDs(got []tags.Tag, want []int64) bool {
	if len(got) != len(want) {
		return false
	}

	g := tagIDs(got)
	sort.Slice(g, func(i, j int) bool { return g[i] < g[j] })
	w := append([]int64{}, want...)
	sort.Slice(w, func(i, j int) bool { return w[i] < w[j] })
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}

	return true
}
