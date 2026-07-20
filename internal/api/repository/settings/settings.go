// Package settings is the persistence layer for the settings table - a generic key/value store for runtime-tweakable configuration.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrSettingNotFound = errors.New("setting not found")

// commentsEnabledKey backs the global comments kill-switch: '0' closes  commenting on every post regardless of posts.comments_enabled; '1' defers to the per-post flag.
const commentsEnabledKey = "comments_enabled"

// Repo wraps a *sql.DB for settings-table queries.
type Repo struct {
	db *sql.DB
}

func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// Get returns the raw value stored under key, or ErrSettingNotFound.
func (r *Repo) Get(ctx context.Context, key string) (string, error) {
	const q = `SELECT value FROM settings WHERE key = ?`
	var value string
	err := r.db.QueryRowContext(ctx, q, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSettingNotFound
	}

	if err != nil {
		return "", fmt.Errorf("get setting %q: %w", key, err)
	}

	return value, nil
}

// Set upserts value under key. The PRIMARY KEY on `key` makes the ON CONFLICT clause a plain overwrite - no read-modify-write race.
func (r *Repo) Set(ctx context.Context, key, value string) error {
	const q = `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`
	if _, err := r.db.ExecContext(ctx, q, key, value); err != nil {
		return fmt.Errorf("set setting %q: %w", key, err)
	}

	return nil
}

// CommentsEnabled reports the global comments kill-switch.
// `False` means commenting is closed on every post for every role (including Admin), regardless of the per-post posts.comments_enabled flag.
func (r *Repo) CommentsEnabled(ctx context.Context) (bool, error) {
	value, err := r.Get(ctx, commentsEnabledKey)
	if err != nil {
		return false, err
	}

	return value == "1", nil
}

// SetCommentsEnabled flips the global comments kill-switch.
func (r *Repo) SetCommentsEnabled(ctx context.Context, enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}

	return r.Set(ctx, commentsEnabledKey, value)
}
