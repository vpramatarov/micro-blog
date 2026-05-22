package tokens

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrRefreshTokenNotFound is returned when FindRefreshToken cannot match the given hash — either the token never existed or it was rotated/revoked.
var ErrRefreshTokenNotFound = errors.New("refresh token not found")

const DB_TABLE string = "refresh_tokens"

type Repo struct {
	db *sql.DB
}

func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) Insert(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s (user_id, token_hash, expires_at) VALUES (?, ?, ?)`, DB_TABLE),
		userID, tokenHash, expiresAt.UTC(),
	)

	if err != nil {
		return fmt.Errorf("insert refresh token: %w", err)
	}

	return nil
}

// Find returns (user_id, expires_at) for the given token hash.
// Before the SELECT it sweeps expired rows — refresh tokens accumulate as users log in / out and there's no scheduler in the binary.
// Indexed on expires_at by migration 00005 so the purge is O(expired-rows).
func (r *Repo) FindOneByHash(ctx context.Context, tokenHash string) (userID int64, expiresAt time.Time, err error) {
	if _, e := r.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE expires_at < ?`, DB_TABLE), time.Now().UTC()); e != nil {
		return 0, time.Time{}, fmt.Errorf("prune expired refresh tokens: %w", e)
	}

	q := fmt.Sprintf(`SELECT user_id, expires_at FROM %s WHERE token_hash = ?`, DB_TABLE)
	err = r.db.QueryRowContext(ctx, q, tokenHash).Scan(&userID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, ErrRefreshTokenNotFound
	}

	if err != nil {
		return 0, time.Time{}, fmt.Errorf("select refresh token: %w", err)
	}

	return userID, expiresAt, nil
}

func (r *Repo) Delete(ctx context.Context, tokenHash string) error {
	_, err := r.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE token_hash = ?`, DB_TABLE), tokenHash)
	if err != nil {
		return fmt.Errorf("delete refresh token: %w", err)
	}

	return nil
}

func (r *Repo) DeleteUserTokens(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE user_id = ?`, DB_TABLE), userID)
	if err != nil {
		return fmt.Errorf("delete user refresh tokens: %w", err)
	}

	return nil
}

// RotateRefreshToken atomically swaps an existing refresh token row for a new one in single transaction.
func (r *Repo) RotateRefreshToken(ctx context.Context, oldHash string, userID int64, newHash string, expiresAt time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback() // Rollback is a no-op once Commit ran.
	}()

	q := fmt.Sprintf("DELETE FROM %s WHERE token_hash = ?", DB_TABLE)
	res, err := tx.ExecContext(ctx, q, oldHash)
	if err != nil {
		return fmt.Errorf("delete old refresh token: %w", err)
	}

	deleted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rotate refresh token: rows affected: %w", err)
	}

	if deleted == 0 {
		return ErrRefreshTokenNotFound
	}

	q = fmt.Sprintf("INSERT INTO %s (user_id, token_hash, expires_at) VALUES (?, ?, ?)", DB_TABLE)
	if _, err := tx.ExecContext(ctx, q, userID, newHash, expiresAt.UTC()); err != nil {
		return fmt.Errorf("insert new refresh token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}
