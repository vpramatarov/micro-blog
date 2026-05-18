package repository

import "strings"

// IsUniqueViolation matches modernc.org/sqlite's wording for UNIQUE constraint
// failures. The driver does not expose a typed error code, so we string-match.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed: UNIQUE")
}

// IsSlugUniqueViolation matches modernc.org/sqlite's wording for UNIQUE
// constraint failures. We only flag it when the error mentions the posts.slug
// index — other UNIQUE columns on posts would funnel into a generic 500.
// 'routeName' Ex.: "posts.slug"
func IsSlugUniqueViolation(err error, routeName string) bool {
	if err == nil {
		return false
	}

	if !IsUniqueViolation(err) {
		return false
	}

	msg := err.Error()
	return strings.Contains(msg, routeName)
}

// IsForeignKeyViolation matches modernc.org/sqlite's wording for FK constraint failures.
// Used to translate ON DELETE RESTRICT bounces into ErrCategoryInUse.
func IsForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	return strings.Contains(msg, "FOREIGN KEY constraint failed") || strings.Contains(msg, "constraint failed: FOREIGN KEY")
}
