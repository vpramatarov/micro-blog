// Package tags is the persistence layer for the tags table and the post_tags
// M:N join table. It owns the Tag row model, the tag error sentinels, and the
// helpers the posts handler uses to validate and hydrate per-post tag lists.
package tags

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vpramatarov/micro-blog/internal/api/repository"
)

const DB_TABLE string = "tags"
const POST_TAG_TABLE string = "post_tags"

// ErrTagNotFound is returned when a SELECT/UPDATE/DELETE targets an id that does not exist.
var ErrTagNotFound = errors.New("tag not found")

// ErrTagDuplicate is returned when an INSERT or UPDATE would collide on (name) — the column is UNIQUE in migration 00006.
var ErrTagDuplicate = errors.New("tag already exists")

// Tag is both the DB row and the JSON view.
type Tag struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Repo wraps a *sql.DB for tags-table and post_tags-table queries.
type Repo struct {
	db *sql.DB
}

func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) Create(ctx context.Context, name string) (int64, error) {
	q := fmt.Sprintf(`INSERT INTO %s (name) VALUES (?)`, DB_TABLE)
	res, err := r.db.ExecContext(ctx, q, name)
	if err != nil {
		if repository.IsUniqueViolation(err) {
			return 0, ErrTagDuplicate
		}

		return 0, fmt.Errorf("insert tag: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	return id, nil
}

func (r *Repo) GetByID(ctx context.Context, id int64) (*Tag, error) {
	q := fmt.Sprintf(`SELECT id, name, created_at FROM %s WHERE id = ?`, DB_TABLE)
	var t Tag
	err := r.db.QueryRowContext(ctx, q, id).Scan(&t.ID, &t.Name, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTagNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("get tag: %w", err)
	}

	return &t, nil
}

func (r *Repo) List(ctx context.Context, limit, offset int) ([]Tag, error) {
	q := fmt.Sprintf(`SELECT id, name, created_at FROM %s ORDER BY name LIMIT ? OFFSET ?`, DB_TABLE)
	rows, err := r.db.QueryContext(ctx, q, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	out := make([]Tag, 0)
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tags: %w", err)
	}

	return out, nil
}

func (r *Repo) Count(ctx context.Context) (int, error) {
	q := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, DB_TABLE)
	var n int
	err := r.db.QueryRowContext(ctx, q).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count tags: %w", err)
	}

	return n, nil
}

// UpdateTag pre-checks existence (same SQLite RowsAffected quirk as
// elsewhere) before issuing the UPDATE.
func (r *Repo) Update(ctx context.Context, id int64, name string) error {
	if _, err := r.GetByID(ctx, id); err != nil {
		return err
	}

	q := fmt.Sprintf(`UPDATE %s SET name = ? WHERE id = ?`, DB_TABLE)
	_, err := r.db.ExecContext(ctx, q, name, id)
	if err != nil {
		if repository.IsUniqueViolation(err) {
			return ErrTagDuplicate
		}

		return fmt.Errorf("update tag: %w", err)
	}

	return nil
}

// DeleteTag removes the tag. post_tags rows referencing it cascade away
// automatically via the FK ON DELETE CASCADE — no in-use error here.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	q := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, DB_TABLE)
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return ErrTagNotFound
	}

	return nil
}

// MissingIDs returns the subset of `ids` that do NOT exist in the tags
// table. Empty slice means all ids are valid. Used by the posts handler to
// validate the `tag_ids` field in one round-trip before insert.
func (r *Repo) MissingIDs(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	q := fmt.Sprintf(`SELECT id FROM %s WHERE id IN (%s)`, DB_TABLE, placeholders)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("missing tag ids: %w", err)
	}
	defer rows.Close()

	present := make(map[int64]struct{}, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan tag id: %w", err)
		}
		present[id] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tag ids: %w", err)
	}

	var missing []int64
	for _, id := range ids {
		if _, ok := present[id]; !ok {
			missing = append(missing, id)
		}
	}

	return missing, nil
}

// ListForPost returns the tags attached to a single post, ordered by name.
// Returns an empty slice (not nil error) when the post has no tags.
func (r *Repo) ListForPost(ctx context.Context, postID int64) ([]Tag, error) {
	q := fmt.Sprintf(`
        SELECT t.id, t.name, t.created_at
        FROM %s t
        JOIN %s pt ON pt.tag_id = t.id
        WHERE pt.post_id = ?
        ORDER BY t.name`,
		DB_TABLE,
		POST_TAG_TABLE)
	rows, err := r.db.QueryContext(ctx, q, postID)
	if err != nil {
		return nil, fmt.Errorf("list tags for post: %w", err)
	}
	defer rows.Close()

	out := make([]Tag, 0)
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tags: %w", err)
	}

	return out, nil
}

// ListTagsForPosts batches ListTagsForPost across a slice of post ids in one
// round-trip, returning a map keyed by post id. Every input id is present in
// the map (possibly with an empty slice). Used by the posts handler to hydrate list responses without an N+1.
func (r *Repo) ListForPosts(ctx context.Context, postIDs []int64) (map[int64][]Tag, error) {
	out := make(map[int64][]Tag, len(postIDs))
	for _, id := range postIDs {
		out[id] = nil
	}

	if len(postIDs) == 0 {
		return out, nil
	}

	placeholders := strings.Repeat("?,", len(postIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(postIDs))
	for i, id := range postIDs {
		args[i] = id
	}

	q := fmt.Sprintf(`
        SELECT pt.post_id, t.id, t.name, t.created_at
        FROM %s t
        JOIN %s pt ON pt.tag_id = t.id
        WHERE pt.post_id IN (%s)
        ORDER BY pt.post_id, t.name`,
		DB_TABLE,
		POST_TAG_TABLE,
		placeholders)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list tags for posts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var postID int64
		var t Tag
		if err := rows.Scan(&postID, &t.ID, &t.Name, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}

		out[postID] = append(out[postID], t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tags: %w", err)
	}

	return out, nil
}

// ReplaceForPost atomically rewrites the join rows for `postID`: deletes
// every existing row, then inserts one per tag in `tagIDs`. Idempotent — call
// with an empty slice to clear the tag set. Wrapped in a transaction so a partial failure leaves the row set unchanged.
func (r *Repo) ReplaceForPost(ctx context.Context, postID int64, tagIDs []int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		// rollback is a no-op if Commit already ran.
		_ = tx.Rollback()
	}()
	
	q := fmt.Sprintf(`DELETE FROM %s WHERE post_id = ?`, POST_TAG_TABLE)
	if _, err := tx.ExecContext(ctx, q, postID); err != nil {
		return fmt.Errorf("clear post tags: %w", err)
	}

	if len(tagIDs) > 0 {
		// Deduplicate to avoid PRIMARY KEY collisions on (post_id, tag_id).
		seen := make(map[int64]struct{}, len(tagIDs))
		for _, tagID := range tagIDs {
			if _, dup := seen[tagID]; dup {
				continue
			}

			seen[tagID] = struct{}{}
			q := fmt.Sprintf(`INSERT INTO %s (post_id, tag_id) VALUES (?, ?)`, POST_TAG_TABLE)
			if _, err := tx.ExecContext(ctx, q, postID, tagID); err != nil {
				return fmt.Errorf("insert post tag: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}
