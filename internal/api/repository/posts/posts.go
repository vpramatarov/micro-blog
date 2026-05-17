package posts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const DB_TABLE string = "posts"

// ErrPostNotFound is returned when a SELECT/UPDATE/DELETE targets an id that does not exist.
var ErrPostNotFound = errors.New("post not found")

// Post is both the DB row and the JSON view.
type Post struct {
	ID              int64     `json:"id"`
	Code            string    `json:"code,omitempty"`
	AuthorID        int64     `json:"author_id"`
	Title           string    `json:"title"`
	MarkdownContent string    `json:"markdown_content"`
	HTMLContent     string    `json:"html_content"`
	CreatedAt       time.Time `json:"created_at"`
}

type Repo struct {
	db *sql.DB
}

func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) GetByID(ctx context.Context, id int64) (*Post, error) {
	q := fmt.Sprintf(`
		SELECT id, author_id, title, markdown_content, html_content, created_at FROM %s WHERE id = ?`,
		DB_TABLE,
	)

	var post Post
	err := r.db.QueryRowContext(ctx, q, id).Scan(
		&post.ID,
		&post.AuthorID,
		&post.Title,
		&post.MarkdownContent,
		&post.HTMLContent,
		&post.CreatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPostNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("get post: %w", err)
	}

	return &post, nil
}

// GetOwnerID is the bouncer's ownership lookup for post:* actions with scope='own'.
// Returns ErrPostNotFound when the post does not exist so the caller can map it to 404.
func (r *Repo) GetOwnerID(ctx context.Context, postID int64) (int64, error) {
	q := fmt.Sprintf(`SELECT author_id FROM %s WHERE id = ?`, DB_TABLE)
	var ownerID int64
	err := r.db.QueryRowContext(ctx, q, postID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrPostNotFound
	}

	return ownerID, err
}

func (r *Repo) Create(ctx context.Context, authorID int64, title, markdown, html string) (int64, error) {
	q := fmt.Sprintf("INSERT INTO %s (author_id, title, markdown_content, html_content) VALUES (?, ?, ?, ?);", DB_TABLE)
	res, err := r.db.ExecContext(ctx, q, authorID, title, markdown, html)
	if err != nil {
		return 0, fmt.Errorf("insert post: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	return id, nil
}

func (r *Repo) Update(ctx context.Context, id int64, title, markdown, html string) error {
	// Pre-check existence — SQLite's RowsAffected on UPDATE counts only rows
	// that actually changed, so a no-op update on an existing row reports 0
	// and would be indistinguishable from a missing row. Same pattern as users.UpdateUser.
	if _, err := r.GetByID(ctx, id); err != nil {
		return err
	}

	updateQ := fmt.Sprintf("UPDATE %s SET title = ?, markdown_content = ?, html_content = ? WHERE id = ?", DB_TABLE)
	if _, err := r.db.ExecContext(ctx, updateQ, title, markdown, html, id); err != nil {
		return fmt.Errorf("update post: %w", err)
	}

	return nil
}

func (r *Repo) Delete(ctx context.Context, id int64) error {
	q := fmt.Sprintf("DELETE FROM %s WHERE id = ?", DB_TABLE)
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete post: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return ErrPostNotFound
	}

	return nil
}

func (r *Repo) Count(ctx context.Context) (int, error) {
	var n int
	q := fmt.Sprintf("SELECT COUNT(*) FROM %s", DB_TABLE)
	err := r.db.QueryRowContext(ctx, q).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count posts: %w", err)
	}

	return n, nil
}

func (r *Repo) CountByAuthor(ctx context.Context, authorID int64) (int, error) {
	var n int
	q := fmt.Sprintf("`SELECT COUNT(*) FROM %s WHERE author_id = ?`", DB_TABLE)
	err := r.db.QueryRowContext(ctx, q, authorID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count posts by author: %w", err)
	}

	return n, nil
}

func (r *Repo) List(ctx context.Context, limit, offset int) ([]Post, error) {
	q := fmt.Sprintf(`
		SELECT id, author_id, title, markdown_content, html_content, created_at
			FROM %s ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		DB_TABLE)

	return r.query(ctx, q, limit, offset)
}

func (r *Repo) ListByAuthor(ctx context.Context, authorID int64, limit, offset int) ([]Post, error) {
	q := fmt.Sprintf(`
		SELECT id, author_id, title, markdown_content, html_content, created_at
			FROM %s WHERE author_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		DB_TABLE)

	return r.query(ctx, q, authorID, limit, offset)
}

func (r *Repo) query(ctx context.Context, sqlQuery string, args ...any) ([]Post, error) {
	rows, err := r.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("query posts: %w", err)
	}
	defer rows.Close()

	posts := make([]Post, 0)
	for rows.Next() {
		var post Post
		if err := rows.Scan(
			&post.ID,
			&post.AuthorID,
			&post.Title,
			&post.MarkdownContent,
			&post.HTMLContent,
			&post.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan post: %w", err)
		}

		posts = append(posts, post)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate posts: %w", err)
	}

	return posts, nil
}
