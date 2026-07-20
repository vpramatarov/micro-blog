package comments_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/repository/comments"
	postsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	usersrepo "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// seedUserAndPost inserts one Subscriber and one published post they author.
// Slug/username/email carry a suffix so tests can seed several without tripping the UNIQUE columns.
func seedUserAndPost(t *testing.T, db *sql.DB, suffix string) (userID, postID int64) {
	t.Helper()
	ctx := t.Context()
	users := usersrepo.New(db)
	posts := postsrepo.New(db)
	userID, err := users.Create(ctx, "commenter-"+suffix, "commenter-"+suffix+"@example.com", "x-hash-x", 4)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	postID, err = posts.Create(ctx, postsrepo.PostInsert{
		AuthorID: userID, CategoryID: 1,
		Title: "Fixture post " + suffix, Slug: "fixture-post-" + suffix,
		Markdown: "long enough markdown", HTML: "<p>long enough markdown</p>",
		Status: "published",
	})
	if err != nil {
		t.Fatalf("seed post: %v", err)
	}

	return userID, postID
}

func TestCreateAndGetComment(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := comments.New(db)
	ctx := t.Context()
	userID, postID := seedUserAndPost(t, db, "a")
	id, err := r.Create(ctx, postID, userID, 0, "first!")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if id == 0 {
		t.Fatal("zero id returned")
	}

	got, err := r.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Content != "first!" {
		t.Errorf("content: got %q", got.Content)
	}

	if got.AuthorName != "commenter-a" {
		t.Errorf("author_name from join: got %q", got.AuthorName)
	}

	if got.ParentID != 0 {
		t.Errorf("top-level comment should have ParentID 0, got %d", got.ParentID)
	}

	if got.PostID != postID || got.UserID != userID {
		t.Errorf("ids: got post=%d user=%d, want post=%d user=%d", got.PostID, got.UserID, postID, userID)
	}
}

func TestCreateReplyRoundTrip(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := comments.New(db)
	ctx := t.Context()
	userID, postID := seedUserAndPost(t, db, "a")
	parentID, err := r.Create(ctx, postID, userID, 0, "parent")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	replyID, err := r.Create(ctx, postID, userID, parentID, "reply")
	if err != nil {
		t.Fatalf("create reply: %v", err)
	}

	got, err := r.Get(ctx, replyID)
	if err != nil {
		t.Fatalf("get reply: %v", err)
	}

	if got.ParentID != parentID {
		t.Errorf("parent id: got %d, want %d", got.ParentID, parentID)
	}
}

func TestGetCommentNotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := comments.New(db)
	if _, err := r.Get(t.Context(), 999_999); !errors.Is(err, comments.ErrCommentNotFound) {
		t.Errorf("got %v, want ErrCommentNotFound", err)
	}
}

func TestListTopLevelByPostExcludesRepliesAndPages(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := comments.New(db)
	ctx := t.Context()
	userID, postID := seedUserAndPost(t, db, "a")
	first, _ := r.Create(ctx, postID, userID, 0, "one")
	second, _ := r.Create(ctx, postID, userID, 0, "two")
	third, _ := r.Create(ctx, postID, userID, 0, "three")
	if _, err := r.Create(ctx, postID, userID, first, "a reply"); err != nil {
		t.Fatalf("create reply: %v", err)
	}

	all, err := r.ListTopLevel(ctx, postID, 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(all) != 3 {
		t.Fatalf("top-level count: got %d, want 3 (replies must be excluded)", len(all))
	}
	// Oldest-first: creation order. created_at has second precision, so the
	// id tiebreaker carries the ordering within a fast test run.
	if all[0].ID != first || all[1].ID != second || all[2].ID != third {
		t.Errorf("order: got [%d %d %d], want [%d %d %d]",
			all[0].ID, all[1].ID, all[2].ID, first, second, third)
	}

	page, err := r.ListTopLevel(ctx, postID, 2, 2)
	if err != nil {
		t.Fatalf("page: %v", err)
	}

	if len(page) != 1 || page[0].ID != third {
		t.Errorf("limit/offset: got %d rows, want the single third comment", len(page))
	}

	total, err := r.CountTopLevel(ctx, postID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}

	if total != 3 {
		t.Errorf("count: got %d, want 3 (replies must be excluded)", total)
	}
}

func TestListRepliesForCommentsBatches(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := comments.New(db)
	ctx := t.Context()
	userID, postID := seedUserAndPost(t, db, "a")
	parentA, _ := r.Create(ctx, postID, userID, 0, "parent A")
	parentB, _ := r.Create(ctx, postID, userID, 0, "parent B")
	parentC, _ := r.Create(ctx, postID, userID, 0, "parent C") // no replies
	replyA1, _ := r.Create(ctx, postID, userID, parentA, "A reply 1")
	replyA2, _ := r.Create(ctx, postID, userID, parentA, "A reply 2")
	replyB1, _ := r.Create(ctx, postID, userID, parentB, "B reply 1")
	out, err := r.ListReplies(ctx, []int64{parentA, parentB, parentC})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	if len(out) != 3 {
		t.Fatalf("map size: got %d, want every input id keyed", len(out))
	}

	if got := out[parentA]; len(got) != 2 || got[0].ID != replyA1 || got[1].ID != replyA2 {
		t.Errorf("parent A replies: got %+v", got)
	}

	if got := out[parentB]; len(got) != 1 || got[0].ID != replyB1 {
		t.Errorf("parent B replies: got %+v", got)
	}

	if got, present := out[parentC]; !present || len(got) != 0 {
		t.Errorf("parent C: want present with no replies, got present=%v len=%d", present, len(got))
	}

	empty, err := r.ListReplies(ctx, nil)
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}

	if len(empty) != 0 {
		t.Errorf("empty input: got %d entries, want 0", len(empty))
	}
}

func TestDeleteCommentCascadesReplies(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := comments.New(db)
	ctx := t.Context()
	userID, postID := seedUserAndPost(t, db, "a")
	parentID, _ := r.Create(ctx, postID, userID, 0, "parent")
	replyID, _ := r.Create(ctx, postID, userID, parentID, "reply")
	if err := r.Delete(ctx, parentID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := r.Get(ctx, replyID); !errors.Is(err, comments.ErrCommentNotFound) {
		t.Errorf("reply after parent delete: got %v, want ErrCommentNotFound (cascade)", err)
	}

	if err := r.Delete(ctx, parentID); !errors.Is(err, comments.ErrCommentNotFound) {
		t.Errorf("second delete: got %v, want ErrCommentNotFound", err)
	}
}

func TestDeletePostCascadesComments(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := comments.New(db)
	posts := postsrepo.New(db)
	ctx := t.Context()
	userID, postID := seedUserAndPost(t, db, "a")
	parentID, _ := r.Create(ctx, postID, userID, 0, "parent")
	if _, err := r.Create(ctx, postID, userID, parentID, "reply"); err != nil {
		t.Fatalf("create reply: %v", err)
	}

	if err := posts.Delete(ctx, postID); err != nil {
		t.Fatalf("delete post: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM comments WHERE post_id = ?`, postID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}

	if n != 0 {
		t.Errorf("comments after post delete: got %d, want 0 (FK cascade)", n)
	}
}
