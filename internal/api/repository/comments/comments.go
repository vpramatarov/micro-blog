// Package comments is the persistence layer for the comments table. It owns the Comment row model and its error sentinel.
package comments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vpramatarov/micro-blog/internal/api/repository"
)

const DB_TABLE string = "comments"

// ErrCommentNotFound is returned when a SELECT/DELETE targets an id that does not exist.
var ErrCommentNotFound = errors.New("comment not found")

// Comment is both the DB row and the JSON view.
// AuthorName comes from the INNER JOIN to users in every read. ParentID is 0 for top-level comments - the nullable column is COALESCE'd so the struct holds a int64
// (mirrors the posts repo's featured_image_path) and omitempty drops it from top-level JSON.
type Comment struct {
	ID         int64     `json:"id"`
	PostID     int64     `json:"post_id"`
	UserID     int64     `json:"user_id"`
	AuthorName string    `json:"author_name"`
	ParentID   int64     `json:"parent_id,omitempty"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
}

// Repo wraps a *sql.DB for comments-table queries.
type Repo struct {
	db *sql.DB
}

func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// commentColumns is the canonical aliased projection used by every Comment read; every consumer pairs it with commentJoins so the order matches scanComment.
const commentColumns = `c.id, c.post_id, c.user_id, u.username, ` +
	`COALESCE(c.parent_id, 0), c.content, c.created_at`

// commentJoins anchors the FROM clause for joined reads. INNER is safe:
// comments.user_id REFERENCES users(id) ON DELETE CASCADE, so the FK can't dangle.
const commentJoins = `FROM ` + DB_TABLE + ` c INNER JOIN users u ON u.id = c.user_id`

// Create inserts a row. parentID == 0 means top-level - the column gets NULL so the self-FK isn't checked against a nonexistent id 0.
// The handler validates parent existence/same-post/top-level before calling.
func (r *Repo) Create(ctx context.Context, postID, userID, parentID int64, content string) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO comments (post_id, user_id, parent_id, content) VALUES (?, ?, ?, ?)`,
		postID, userID, repository.NullableID(parentID), content,
	)

	if err != nil {
		return 0, fmt.Errorf("insert comment: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	return id, nil
}

// Get returns the joined row. Used by the delete handler's authz check and by CreateComment's parent validation (both need PostID/UserID/ParentID in one read).
func (r *Repo) Get(ctx context.Context, id int64) (*Comment, error) {
	q := `SELECT ` + commentColumns + ` ` + commentJoins + ` WHERE c.id = ?`
	var cm Comment
	err := scanComment(r.db.QueryRowContext(ctx, q, id), &cm)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCommentNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("get comment: %w", err)
	}

	return &cm, nil
}

// ListTopLevel returns the page of top-level comments for a post in oldest-first order - conversation order, a deliberate divergence from the posts lists' newest-first.
// Replies are fetched separately via ListRepliesForComments.
func (r *Repo) ListTopLevel(ctx context.Context, postID int64, limit, offset int) ([]Comment, error) {
	q := `SELECT ` + commentColumns + ` ` + commentJoins +
		` WHERE c.post_id = ? AND c.parent_id IS NULL
		  ORDER BY c.created_at ASC, c.id ASC LIMIT ? OFFSET ?`
	return r.queryComments(ctx, q, postID, limit, offset)
}

// CountTopLevel backs the pagination `total` of the comments list -  top-level rows only, NOT the same number as posts.comment_count (which counts replies too).
func (r *Repo) CountTopLevel(ctx context.Context, postID int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM comments WHERE post_id = ? AND parent_id IS NULL`, postID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count top-level comments: %w", err)
	}

	return n, nil
}

// ListReplies batches the replies lookup across a page of parent ids in one round-trip, returning a map keyed by parent id.
// Every input id is present in the map (possibly with a nil slice) - same contract as tags repo `ListTagsForPosts`.
// Replies are oldest-first within each parent.
func (r *Repo) ListReplies(ctx context.Context, parentIDs []int64) (map[int64][]Comment, error) {
	out := make(map[int64][]Comment, len(parentIDs))
	for _, id := range parentIDs {
		out[id] = nil
	}

	if len(parentIDs) == 0 {
		return out, nil
	}

	placeholders := strings.Repeat("?,", len(parentIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(parentIDs))
	for i, id := range parentIDs {
		args[i] = id
	}

	q := `SELECT ` + commentColumns + ` ` + commentJoins +
		` WHERE c.parent_id IN (` + placeholders + `)
		  ORDER BY c.parent_id, c.created_at ASC, c.id ASC`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list replies for comments: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cm Comment
		if err := scanComment(rows, &cm); err != nil {
			return nil, fmt.Errorf("scan comment: %w", err)
		}
		out[cm.ParentID] = append(out[cm.ParentID], cm)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate comments: %w", err)
	}

	return out, nil
}

// Delete removes the row; replies cascade at the DB via the parent_id self-FK.
// RowsAffected is reliable here - the SQLite changed-vs-matched quirk only affects UPDATE.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM comments WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return ErrCommentNotFound
	}

	return nil
}

// scanComment consumes commentColumns in order from any row-shaped source.
func scanComment(s interface{ Scan(...any) error }, cm *Comment) error {
	return s.Scan(
		&cm.ID, &cm.PostID, &cm.UserID, &cm.AuthorName,
		&cm.ParentID, &cm.Content, &cm.CreatedAt,
	)
}

func (r *Repo) queryComments(ctx context.Context, q string, args ...any) ([]Comment, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query comments: %w", err)
	}
	defer rows.Close()

	comments := make([]Comment, 0)
	for rows.Next() {
		var cm Comment
		if err := scanComment(rows, &cm); err != nil {
			return nil, fmt.Errorf("scan comment: %w", err)
		}

		comments = append(comments, cm)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate comments: %w", err)
	}

	return comments, nil
}
