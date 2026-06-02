package slug

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

type Table struct{ name string }

// Pre-declared sentinels for every table that owns a UNIQUE slug column.
var (
	TablePosts      = Table{"posts"}
	TableCategories = Table{"categories"}
	TableTags       = Table{"tags"}
)

// Finder allocates non-colliding kebab-case slugs against a single table that has an INTEGER PRIMARY KEY `id` and a UNIQUE TEXT `slug` column.
type Finder struct {
	db        *sql.DB
	selectSQL string
}

func NewFinder(db *sql.DB, t Table) *Finder {
	return &Finder{
		db: db,
		selectSQL: fmt.Sprintf(
			`SELECT slug FROM %s WHERE (slug = ? OR slug LIKE ?) AND id != ?`, t.name),
	}
}

// Generate returns either `base` itself or the smallest `base-N` (N≥2)  that is unclaimed in the configured table.
// Pass 0 for excludeID on insert; on update pass the row's id so its existing slug doesn't count as a self-collision.
//
// `base` is the kebab-case output of Generate, which only emits [a-z0-9-].
// SQL LIKE wildcards ('%', '_') can never appear in it, so `base+"-%"` is safe as a prefix filter.
func (f *Finder) Generate(ctx context.Context, base string, excludeID int64) (string, error) {
	rows, err := f.db.QueryContext(ctx, f.selectSQL, base, base+"-%", excludeID)
	if err != nil {
		return "", fmt.Errorf("find available slug: %w", err)
	}
	defer rows.Close()

	taken := make(map[string]struct{})
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", fmt.Errorf("scan slug: %w", err)
		}

		taken[s] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate slugs: %w", err)
	}

	if _, conflict := taken[base]; !conflict {
		return base, nil
	}

	for i := 2; ; i++ {
		candidate := base + "-" + strconv.Itoa(i)
		if _, conflict := taken[candidate]; !conflict {
			return candidate, nil
		}
	}
}
