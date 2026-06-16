package posts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vpramatarov/micro-blog/internal/api/repository"
	"github.com/vpramatarov/micro-blog/internal/slug"
)

const DB_TABLE string = "posts"

const POSTS_SELECT_COLUMS string = `
	p.id,
	p.author_id,
	u.username,
	p.category_id,
	c.name,
	p.title,
	p.slug,
	p.markdown_content,
	p.html_content,
	p.created_at,
	COALESCE(p.featured_image_path, ''),
	p.status
`

const (
	PostStatusDraft     string = "draft"
	PostStatusPublished string = "published"
	PostStatusArchived  string = "archived"
)

// ErrPostNotFound is returned when a SELECT/UPDATE/DELETE targets an id that does not exist.
var ErrPostNotFound = errors.New("post not found")

// ErrPostDuplicateSlug is returned when an INSERT or UPDATE collides on the UNIQUE slug index.
// The handler resolves base collisions before insert via FindAvailableSlug;
// this sentinel only fires in the tiny race window between two concurrent writers that picked the same suffix.
var ErrPostDuplicateSlug = errors.New("post slug already exists")

// Post is both the DB row and the JSON view.
type Post struct {
	ID                int64     `json:"id"`
	Code              string    `json:"code,omitempty"`
	AuthorID          int64     `json:"author_id"`
	AuthorName        string    `json:"author_name,omitempty"`
	CategoryID        int64     `json:"category_id"`
	CategoryName      string    `json:"category_name,omitempty"`
	Title             string    `json:"title"`
	Slug              string    `json:"slug"`
	MarkdownContent   string    `json:"markdown_content"`
	HTMLContent       string    `json:"html_content"`
	CreatedAt         time.Time `json:"created_at"`
	FeaturedImagePath string    `json:"featured_image_path,omitempty"`
	Status            string    `json:"status"`
}

// PostInsert carries the fields CreatePost writes.
// Bundled into a struct so the call site doesn't sprout a long positional argument list each time the schema grows.
// FeaturedImagePath is "" when no image was uploaded; the repo writes NULL to the column in that case.
type PostInsert struct {
	AuthorID          int64
	CategoryID        int64
	Title             string
	Markdown          string
	HTML              string
	Slug              string
	FeaturedImagePath string
	Status            string
}

// PostUpdate carries the fields UpdatePost rewrites. category_id and slug always come from the handler — there is no partial-update mode.
// The handler computes FeaturedImagePath as one of: empty (clear), the existing path (keep), or the new path (replace).
type PostUpdate struct {
	CategoryID        int64
	Title             string
	Markdown          string
	HTML              string
	Slug              string
	FeaturedImagePath string
	Status            string
}

type Repo struct {
	db         *sql.DB
	slugFinder *slug.Finder
}

func New(db *sql.DB) *Repo {
	return &Repo{db: db, slugFinder: slug.NewFinder(db, slug.TablePosts)}
}

func (r *Repo) GetByID(ctx context.Context, id int64) (*Post, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM %s AS p
		INNER JOIN users AS u ON u.id = p.author_id
		INNER JOIN categories AS c ON c.id = p.category_id
		WHERE p.id = ?`,
		POSTS_SELECT_COLUMS, DB_TABLE)

	var post Post
	err := r.db.QueryRowContext(ctx, q, id).Scan(
		&post.ID,
		&post.AuthorID,
		&post.AuthorName,
		&post.CategoryID,
		&post.CategoryName,
		&post.Title,
		&post.Slug,
		&post.MarkdownContent,
		&post.HTMLContent,
		&post.CreatedAt,
		&post.FeaturedImagePath,
		&post.Status,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPostNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("get post: %w", err)
	}

	return &post, nil
}

// GetBySlug is the read path behind GET /posts/{slug}. Public — the slug is taken from the URL and looked up directly.
func (r *Repo) GetBySlug(ctx context.Context, slugStr string) (*Post, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM %s AS p
		INNER JOIN users AS u ON u.id = p.author_id
		INNER JOIN categories AS c ON c.id = p.category_id
		WHERE p.slug = ? AND p.status = '%s'`,
		POSTS_SELECT_COLUMS, DB_TABLE, PostStatusPublished)
	var p Post
	err := r.db.QueryRowContext(ctx, q, slugStr).Scan(
		&p.ID,
		&p.AuthorID,
		&p.AuthorName,
		&p.CategoryID,
		&p.CategoryName,
		&p.Title,
		&p.Slug,
		&p.MarkdownContent,
		&p.HTMLContent,
		&p.CreatedAt,
		&p.FeaturedImagePath,
		&p.Status,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPostNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("get post by slug: %w", err)
	}

	return &p, nil
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

func (r *Repo) Create(ctx context.Context, p PostInsert) (int64, error) {
	if p.Status == "" {
		p.Status = PostStatusDraft
	}

	q := fmt.Sprintf(`
		INSERT INTO %s (author_id, category_id, title, slug, markdown_content, html_content, featured_image_path, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?);`,
		DB_TABLE)
	res, err := r.db.ExecContext(ctx, q, p.AuthorID, p.CategoryID, p.Title, p.Slug, p.Markdown, p.HTML, repository.NullableString(p.FeaturedImagePath), p.Status)
	if err != nil {
		if repository.IsSlugUniqueViolation(err, "posts.slug") {
			return 0, ErrPostDuplicateSlug
		}

		return 0, fmt.Errorf("insert post: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	return id, nil
}

func (r *Repo) Update(ctx context.Context, id int64, post PostUpdate) error {
	// Pre-check existence — SQLite's RowsAffected on UPDATE counts only rows
	// that actually changed, so a no-op update on an existing row reports 0
	// and would be indistinguishable from a missing row. Same pattern as users.UpdateUser.
	if _, err := r.GetByID(ctx, id); err != nil {
		return err
	}

	updateQ := fmt.Sprintf(`
		UPDATE %s SET category_id = ?, title = ?, slug = ?, markdown_content = ?, html_content = ?, featured_image_path = ?, status = ?
		WHERE id = ?`,
		DB_TABLE)
	_, err := r.db.ExecContext(
		ctx, updateQ, post.CategoryID, post.Title, post.Slug, post.Markdown, post.HTML, repository.NullableString(post.FeaturedImagePath), post.Status, id,
	)
	if err != nil {
		if repository.IsSlugUniqueViolation(err, "posts.slug") {
			return ErrPostDuplicateSlug
		}

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

func (r *Repo) Count(ctx context.Context, status string) (int, error) {
	args := []any{}
	var n int
	q := fmt.Sprintf("SELECT COUNT(*) FROM %s", DB_TABLE)
	if status != "" {
		q += ` WHERE status = ? `
		args = append(args, status)
	}

	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count posts: %w", err)
	}

	return n, nil
}

func (r *Repo) CountByAuthor(ctx context.Context, authorID int64, status string) (int, error) {
	args := []any{authorID}
	var n int
	q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE author_id = ?", DB_TABLE)
	if status != "" {
		q += ` WHERE status = ? `
		args = append(args, status)
	}

	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count posts by author: %w", err)
	}

	return n, nil
}

func (r *Repo) List(ctx context.Context, status string, limit, offset int) ([]Post, error) {
	args := []any{}
	q := fmt.Sprintf(`
		SELECT %s FROM %s AS p
		INNER JOIN users AS u ON u.id = p.author_id
		INNER JOIN categories AS c ON c.id = p.category_id`,
		POSTS_SELECT_COLUMS, DB_TABLE)

	if status != "" {
		q += ` WHERE p.status = ? `
		args = append(args, status)
	}

	q += ` ORDER BY p.created_at DESC, p.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return r.query(ctx, q, args...)
}

func (r *Repo) ListByAuthor(ctx context.Context, authorID int64, status string, limit, offset int) ([]Post, error) {
	args := []any{authorID}
	q := fmt.Sprintf(`
		SELECT %s FROM %s AS p
		INNER JOIN users AS u ON u.id = p.author_id
		INNER JOIN categories AS c ON c.id = p.category_id
		WHERE p.author_id = ?`,
		POSTS_SELECT_COLUMS, DB_TABLE)

	if status != "" {
		q += ` AND p.status = ? `
		args = append(args, status)
	}

	q += ` ORDER BY p.created_at DESC, p.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return r.query(ctx, q, args...)
}

// ListByCategoryID returns the page of posts in `categoryID`, newest-first.
// Both `authorID` and `status` are "0 / empty = no filter"
func (r *Repo) ListByCategoryID(ctx context.Context, categoryID, authorID int64, status string, limit, offset int) ([]Post, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM %s AS p
		INNER JOIN users AS u ON u.id = p.author_id
		INNER JOIN categories AS c ON c.id = p.category_id
		WHERE p.category_id = ?`,
		POSTS_SELECT_COLUMS, DB_TABLE)

	args := []any{categoryID}
	if authorID != 0 {
		q += ` AND p.author_id = ?`
		args = append(args, authorID)
	}

	if status != "" {
		q += ` AND p.status = ?`
		args = append(args, status)
	}

	q += ` ORDER BY p.created_at DESC, p.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return r.query(ctx, q, args...)
}

func (r *Repo) CountByCategoryID(ctx context.Context, categoryID, authorID int64, status string) (int, error) {
	q := `SELECT COUNT(*) FROM posts WHERE category_id = ?`
	args := []any{categoryID}
	if authorID != 0 {
		q += ` AND author_id = ?`
		args = append(args, authorID)
	}

	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}

	var n int
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count posts by category: %w", err)
	}

	return n, nil
}

// ListByTagID returns the page of posts attached to `tagID`.
// The M:N join goes through post_tags;
// idx_post_tags_tag keeps the reverse-direction lookup indexed. authorID / status sentinels mirror ListPostsByCategoryID.
func (r *Repo) ListByTagID(ctx context.Context, tagID, authorID int64, status string, limit, offset int) ([]Post, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM %s AS p
		INNER JOIN users AS u ON u.id = p.author_id
		INNER JOIN categories AS c ON c.id = p.category_id
		INNER JOIN post_tags pt ON pt.post_id = p.id
		WHERE pt.tag_id = ?`,
		POSTS_SELECT_COLUMS, DB_TABLE)
	args := []any{tagID}
	if authorID != 0 {
		q += ` AND p.author_id = ?`
		args = append(args, authorID)
	}

	if status != "" {
		q += ` AND p.status = ?`
		args = append(args, status)
	}

	q += ` ORDER BY p.created_at DESC, p.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return r.query(ctx, q, args...)
}

func (r *Repo) CountByTagID(ctx context.Context, tagID, authorID int64, status string) (int, error) {
	q := `SELECT COUNT(*) FROM posts p INNER JOIN post_tags pt ON pt.post_id = p.id WHERE pt.tag_id = ?`
	args := []any{tagID}
	if authorID != 0 {
		q += ` AND p.author_id = ?`
		args = append(args, authorID)
	}

	if status != "" {
		q += ` AND p.status = ?`
		args = append(args, status)
	}

	var n int
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count posts by tag: %w", err)
	}

	return n, nil
}

// GenerateSlug returns either `base` itself or the smallest `base-N` (N≥2) that does not already exist in the posts table.
// `excludePostID` lets an UPDATE keep its own slug — pass 0 from CreatePost.
//
// The query reads every slug in the {base, base-%} family in one round-trip, so collision resolution is O(1) DB hits regardless of how many siblings already exist.
// A concurrent writer can still race us between the SELECT and the INSERT — the UNIQUE index catches that and the handler retries.
func (r *Repo) GenerateSlug(ctx context.Context, base string, excludePostID int64) (string, error) {
	return r.slugFinder.Generate(ctx, base, excludePostID)
}

// Search return sthe page of posts matchinq `q` in title, markdown body or author username, newest-first.
func (r *Repo) Search(ctx context.Context, q, status string, limit, offset int) ([]Post, error) {
	pattern := searchLikePattern(q)
	sql := fmt.Sprintf(`
		SELECT %s FROM %s AS p
		INNER JOIN users AS u ON u.id = p.author_id
		INNER JOIN categories AS c ON c.id = p.category_id
		WHERE (
			p.title LIKE ? ESCAPE '\' OR
			p.markdown_content LIKE ? ESCAPE '\' OR
			u.username LIKE ? ESCAPE '\'
		)`, POSTS_SELECT_COLUMS, DB_TABLE)
	args := []any{pattern, pattern, pattern}
	if status != "" {
		sql += ` AND p.status = ?`
		args = append(args, status)
	}

	sql += ` ORDER BY p.created_at DESC, p.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return r.query(ctx, sql, args...)
}

// CountSearch is the total of Search().
func (r *Repo) CountSearch(ctx context.Context, q, status string) (int, error) {
	pattern := searchLikePattern(q)
	sql := fmt.Sprintf(`
		SELECT COUNT(*) FROM %s AS p
		INNER JOIN users AS u ON u.id = p.author_id
		WHERE (
			p.title LIKE ? ESCAPE '\' OR
			p.markdown_content LIKE ? ESCAPE '\' OR
			u.username LIKE ? ESCAPE '\'
		)`, DB_TABLE)
	args := []any{pattern, pattern, pattern}
	if status != "" {
		sql += ` AND p.status = ?`
		args = append(args, status)
	}

	var n int
	if err := r.db.QueryRowContext(ctx, sql, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count search posts: %w", err)
	}

	return n, nil
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
			&post.AuthorName,
			&post.CategoryID,
			&post.CategoryName,
			&post.Title,
			&post.Slug,
			&post.MarkdownContent,
			&post.HTMLContent,
			&post.CreatedAt,
			&post.FeaturedImagePath,
			&post.Status,
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

// Wraps a query as literal Like term. Meta characters % _ \ are escaped.
func searchLikePattern(q string) string {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + esc.Replace(q) + "%"
}
