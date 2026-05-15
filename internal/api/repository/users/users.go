package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vpramatarov/micro-blog/internal/api/repository"
)

var ErrUserNotFound = errors.New("user not found")
var ErrUserDuplicate = errors.New("user already exists")

const DB_TABLE string = "users"

// User serves both as the DB row model and the JSON view.
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	RoleID       int64  `json:"role_id"`
	RoleName     string `json:"role"`
}

// UserUpdate carries the optional fields for PATCH-style user updates. A nil field is left untouched.
type UserUpdate struct {
	Username     *string
	Email        *string
	PasswordHash *string
	RoleID       *int64
}

type Repository struct {
	db *sql.DB
}

// Repo wraps a *sql.DB for users-table queries.
type Repo struct {
	db *sql.DB
}

// New constructs a Repo. It does not own the connection lifecycle — the caller
// (cmd/server/main.go) keeps responsibility for sql.Open / db.Close.
func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) Create(ctx context.Context, username, email, passwordHash string, roleID int64) (int64, error) {
	query := fmt.Sprintf("INSERT INTO %s (username, email, password_hash, role_id) VALUES (?, ?, ?, ?)", DB_TABLE)
	res, err := r.db.ExecContext(ctx, query, username, email, passwordHash, roleID)
	if err != nil {
		if repository.IsUniqueViolation(err) {
			return 0, ErrUserDuplicate
		}

		return 0, fmt.Errorf("insert user: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	return id, nil
}

func (r *Repo) GetByEmail(ctx context.Context, email string) (*User, error) {
	const q = `
        SELECT u.id, u.username, u.email, u.password_hash, u.role_id, r.name
        FROM %s u
        JOIN roles r ON r.id = u.role_id
        WHERE u.email = ?`
	query := fmt.Sprintf(q, DB_TABLE)
	var u User
	err := r.db.QueryRowContext(ctx, query, email).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.RoleID, &u.RoleName,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("select user by email: %w", err)
	}

	return &u, nil
}

func (r *Repo) GetByID(ctx context.Context, id int64) (*User, error) {
	const q = `
        SELECT u.id, u.username, u.email, u.password_hash, u.role_id, r.name
        FROM %s u
        JOIN roles r ON r.id = u.role_id
        WHERE u.id = ?`
	query := fmt.Sprintf(q, DB_TABLE)
	var u User
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.RoleID, &u.RoleName,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("select user by id: %w", err)
	}

	return &u, nil
}

func (r *Repo) List(ctx context.Context, limit, offset int) ([]User, error) {
	const q = `
        SELECT u.id, u.username, u.email, u.password_hash, u.role_id, r.name
        FROM %s u
        JOIN roles r ON r.id = u.role_id
        ORDER BY u.id
        LIMIT ? OFFSET ?`
	query := fmt.Sprintf(q, DB_TABLE)
	rows, err := r.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]User, 0)
	for rows.Next() {
		var u User
		err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.RoleID, &u.RoleName)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}

		users = append(users, u)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}

	return users, nil
}

func (r *Repo) Count(ctx context.Context) (int, error) {
	var n int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", DB_TABLE)
	err := r.db.QueryRowContext(ctx, query).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}

	return n, nil
}

// UpdateUser applies a partial update. Returns ErrUserNotFound if no row
// exists at id, and ErrUserDuplicate if the update would collide on
// username/email. Pre-checks existence because SQLite's RowsAffected on
// UPDATE counts only rows that actually changed, so a no-op update on an
// existing row reports 0 — indistinguishable from a missing user without
// the pre-check.
func (r *Repo) Update(ctx context.Context, id int64, u UserUpdate) error {
	if _, err := r.GetByID(ctx, id); err != nil {
		return err
	}

	sets := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if u.Username != nil {
		sets = append(sets, "username = ?")
		args = append(args, *u.Username)
	}

	if u.Email != nil {
		sets = append(sets, "email = ?")
		args = append(args, *u.Email)
	}

	if u.PasswordHash != nil {
		sets = append(sets, "password_hash = ?")
		args = append(args, *u.PasswordHash)
	}

	if u.RoleID != nil {
		sets = append(sets, "role_id = ?")
		args = append(args, *u.RoleID)
	}

	if len(sets) == 0 {
		return nil
	}

	args = append(args, id)

	q := "UPDATE %s SET %s WHERE id = ?"
	query := fmt.Sprintf(q, DB_TABLE, strings.Join(sets, ", "))
	if _, err := r.db.ExecContext(ctx, query, args...); err != nil {
		if repository.IsUniqueViolation(err) {
			return ErrUserDuplicate
		}

		return fmt.Errorf("update user: %w", err)
	}

	return nil
}

func (r *Repo) Delete(ctx context.Context, id int64) error {
	q := `DELETE FROM %s WHERE id = ?`
	query := fmt.Sprintf(q, DB_TABLE)
	res, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return ErrUserNotFound
	}

	return nil
}
