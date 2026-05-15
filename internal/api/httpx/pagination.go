package httpx

import (
	"net/http"
	"strconv"
)

// Pagination defaults / bounds, used by every list endpoint via ParsePagination.
const (
	DefaultPerPage = 50
	MaxPerPage     = 200
)

// Page wraps a list response with the offset/total metadata clients need to
// drive a paginated UI without inferring it from the array length.
type Page[T any] struct {
	Items   []T `json:"items"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

// ParsePagination reads `?page` and `?per_page` query params. Missing or
// blank values fall back to (1, DefaultPerPage). Non-numeric or non-positive
// values are rejected as a 400 invalid_pagination — surfaces a clear error
// rather than silently clamping garbage. Per-page is clamped to MaxPerPage.
// Returns (limit, offset, page, perPage, ok); when ok is false the handler
// has already written the 400 and must return.
func ParsePagination(w http.ResponseWriter, r *http.Request) (limit, offset, page, perPage int, ok bool) {
	page = 1
	perPage = DefaultPerPage

	if raw := r.URL.Query().Get("page"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			WriteError(w, http.StatusBadRequest, "invalid_pagination", "page must be a positive integer")
			return 0, 0, 0, 0, false
		}
		page = v
	}
	if raw := r.URL.Query().Get("per_page"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			WriteError(w, http.StatusBadRequest, "invalid_pagination", "per_page must be a positive integer")
			return 0, 0, 0, 0, false
		}
		if v > MaxPerPage {
			v = MaxPerPage
		}
		perPage = v
	}
	return perPage, (page - 1) * perPage, page, perPage, true
}
