// Package categories is the persistence layer for the categories table. It owns the Category row model and the category-specific error sentinels.
package categories

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vpramatarov/micro-blog/internal/api/repository"
)

const DB_TABLE string = "categories"

// ErrCategoryNotFound is returned when a SELECT/UPDATE/DELETE targets an id or name that does not exist.
var ErrCategoryNotFound = errors.New("category not found")

// ErrCategoryDuplicate is returned when an INSERT or UPDATE would collide on (name) — the column is UNIQUE in migration 00006.
var ErrCategoryDuplicate = errors.New("category already exists")

// ErrCategoryInUse is returned by DeleteCategory when posts still reference the row.
// The FK from posts.category_id is ON DELETE RESTRICT so SQLite refuses the DELETE; this sentinel surfaces that to the handler.
var ErrCategoryInUse = errors.New("category in use by posts")

// Category is both the DB row and the JSON view.
type Category struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Repo wraps a *sql.DB for categories-table queries.
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
			return 0, ErrCategoryDuplicate
		}

		return 0, fmt.Errorf("insert category: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	return id, nil
}

func (r *Repo) GetById(ctx context.Context, id int64) (*Category, error) {
	q := fmt.Sprintf(`SELECT id, name, created_at FROM %s WHERE id = ?`, DB_TABLE)
	var c Category
	err := r.db.QueryRowContext(ctx, q, id).Scan(&c.ID, &c.Name, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCategoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get category: %w", err)
	}
	return &c, nil
}

// GetByIDs returns the rows for `ids` keyed by id. Used by the posts handler to hydrate Post.Category on list responses with one round-trip (instead of N).
func (r *Repo) GetByIds(ctx context.Context, ids []int64) (map[int64]Category, error) {
	out := make(map[int64]Category, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	q := `SELECT id, name, created_at FROM categories WHERE id IN (` + placeholders + `)`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("get categories by ids: %w", err)
	}

	defer rows.Close()
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}

		out[c.ID] = c
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate categories: %w", err)
	}

	return out, nil
}

func (r *Repo) List(ctx context.Context, limit, offset int) ([]Category, error) {
	q := fmt.Sprintf(`SELECT id, name, created_at FROM %s ORDER BY name LIMIT ? OFFSET ?`, DB_TABLE)
	rows, err := r.db.QueryContext(ctx, q, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()

	out := make([]Category, 0)
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}

		out = append(out, c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate categories: %w", err)
	}

	return out, nil
}

func (r *Repo) Count(ctx context.Context) (int, error) {
	q := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, DB_TABLE)
	var n int
	err := r.db.QueryRowContext(ctx, q).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count categories: %w", err)
	}

	return n, nil
}

// UpdateCategory replaces the name. Pre-checks existence for the same reason
// as the rest of the repo layer — SQLite's RowsAffected on UPDATE counts only
// rows that actually changed, so a no-op update on an existing row reports 0.
func (r *Repo) Update(ctx context.Context, id int64, name string) error {
	if _, err := r.GetById(ctx, id); err != nil {
		return err
	}

	q := fmt.Sprintf(`UPDATE %s SET name = ? WHERE id = ?`, DB_TABLE)
	_, err := r.db.ExecContext(ctx, q, name, id)
	if err != nil {
		if repository.IsUniqueViolation(err) {
			return ErrCategoryDuplicate
		}

		return fmt.Errorf("update category: %w", err)
	}

	return nil
}

// Delete removes the row. Returns ErrCategoryInUse when the FK from
// posts.category_id (ON DELETE RESTRICT) refuses the DELETE — i.e., one or
// more posts still reference this category. The handler maps that to 409 category_in_use.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	q := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, DB_TABLE)
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		if repository.IsForeignKeyViolation(err) {
			return ErrCategoryInUse
		}
		return fmt.Errorf("delete category: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return ErrCategoryNotFound
	}
	return nil
}

// Exists is the existence check used by the posts handler's category_id validation pass. Cheaper than fetching the full row.
func (r *Repo) Exists(ctx context.Context, id int64) (bool, error) {
	var one int
	q := fmt.Sprintf(`SELECT 1 FROM %s WHERE id = ?`, DB_TABLE)
	err := r.db.QueryRowContext(ctx, q, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("category exists: %w", err)
	}

	return true, nil
}
