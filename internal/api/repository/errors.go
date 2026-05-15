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
