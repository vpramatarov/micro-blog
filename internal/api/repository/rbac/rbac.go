package rbac

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Repo wraps a *sql.DB for RBAC-table queries.
type Repo struct {
	db *sql.DB
}

func New(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// RoleExists reports whether a role row with the given id is present. The
// schema declares role_id as REFERENCES roles(id) but SQLite enforces FKs
// only when PRAGMA foreign_keys is on, so handlers that take role_id from
// untrusted input should validate first.
func (r *Repo) RoleExists(ctx context.Context, id int64) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM roles WHERE id = ?`, id).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("role exists: %w", err)
	}

	return n > 0, nil
}

// GetRolePermissionScope returns the scope ('all', 'own', 'none') for a given
// (role, permission) pair. An empty string means the role has no row for that
// permission — treat it the same as 'none' at the caller.
func (r *Repo) GetRolePermissionScope(ctx context.Context, roleID int64, permission string) (string, error) {
	const q = `
        SELECT rp.scope
        FROM role_permissions rp
        JOIN permissions p ON p.id = rp.permission_id
        WHERE rp.role_id = ? AND p.name = ?`
	var scope string
	err := r.db.QueryRowContext(ctx, q, roleID, permission).Scan(&scope)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("select role permission scope: %w", err)
	}

	return scope, nil
}
