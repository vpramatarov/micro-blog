// Package shortlinks is the persistence layer for the short_links table. It
// owns the ShortLink row model and its error sentinel.
package shortlinks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const DB_TABLE string = "short_links"

// ErrShortLinkNotFound is returned when a SELECT/UPDATE/DELETE targets an id that does not exist.
var ErrShortLinkNotFound = errors.New("short link not found.")

// ShortLink is both the DB row and the JSON view. Code is not persisted —
// handlers populate it before serializing by encoding ID with shortcode.Encoder.
type ShortLink struct {
	ID          int64     `json:"id"`
	Code        string    `json:"code,omitempty"`
	UserID      int64     `json:"user_id"`
	OriginalURL string    `json:"original_url"`
	CreatedAt   time.Time `json:"created_at"`
}

// Repo wraps a *sql.DB for short_links-table queries.
type Repo struct {
	db *sql.DB
}

func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// GetShortLinkOwnerID is the bouncer's ownership lookup for shortlink:edit / shortlink:delete actions with scope='own'.
func (r *Repo) GetOwnerID(ctx context.Context, id int64) (int64, error) {
	var ownerID int64
	q := fmt.Sprintf("SELECT user_id FROM %s WHERE id = ?", DB_TABLE)
	err := r.db.QueryRowContext(ctx, q, id).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrShortLinkNotFound
	}

	return ownerID, err
}

func (r *Repo) Get(ctx context.Context, id int64) (*ShortLink, error) {
	var s ShortLink
	q := fmt.Sprintf("SELECT id, user_id, original_url, created_at FROM %s WHERE id = ?", DB_TABLE)
	err := r.db.QueryRowContext(ctx, q, id).Scan(&s.ID, &s.UserID, &s.OriginalURL, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrShortLinkNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("get short link: %w", err)
	}

	return &s, nil
}

func (r *Repo) List(ctx context.Context, limit, offset int) ([]ShortLink, error) {
	q := fmt.Sprintf(`SELECT id, user_id, original_url, created_at FROM %s ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, DB_TABLE)
	return r.query(ctx, q, limit, offset)
}

func (r *Repo) ListByUser(ctx context.Context, userID int64, limit, offset int) ([]ShortLink, error) {
	q := fmt.Sprintf(`SELECT id, user_id, original_url, created_at FROM %s WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, DB_TABLE)
	return r.query(ctx, q, userID, limit, offset)
}

func (r *Repo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, DB_TABLE)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count short links: %w", err)
	}

	return n, nil
}

func (r *Repo) CountByUser(ctx context.Context, userID int64) (int, error) {
	q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE user_id = ?`, DB_TABLE)
	var n int
	err := r.db.QueryRowContext(ctx, q, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count short links by user: %w", err)
	}

	return n, nil
}

func (r *Repo) Create(ctx context.Context, userID int64, originalURL string) (int64, error) {
	q := fmt.Sprintf("INSERT INTO %s (user_id, original_url) VALUES (?, ?)", DB_TABLE)
	res, err := r.db.ExecContext(ctx, q, userID, originalURL)

	if err != nil {
		return 0, fmt.Errorf("insert short link: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	return id, nil
}

func (r *Repo) Update(ctx context.Context, id int64, originalURL string) error {
	// Pre-check existence — SQLite's RowsAffected on UPDATE counts only rows that actually changed,
	// so a no-op update on an existing row reports 0 and would be indistinguishable from a missing row.
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}

	q := fmt.Sprintf(`UPDATE %s SET original_url = ? WHERE id = ?`, DB_TABLE)
	if _, err := r.db.ExecContext(ctx, q, originalURL, id); err != nil {
		return fmt.Errorf("update short link: %w", err)
	}

	return nil
}

func (r *Repo) Delete(ctx context.Context, id int64) error {
	q := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, DB_TABLE)
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete short link: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return ErrShortLinkNotFound
	}

	return nil
}

func (r *Repo) query(ctx context.Context, q string, args ...any) ([]ShortLink, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query short links: %w", err)
	}
	defer rows.Close()

	links := make([]ShortLink, 0)
	for rows.Next() {
		var s ShortLink
		if err := rows.Scan(&s.ID, &s.UserID, &s.OriginalURL, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan short link: %w", err)
		}

		links = append(links, s)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate short links: %w", err)
	}

	return links, nil
}
