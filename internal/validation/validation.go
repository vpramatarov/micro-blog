// Package validation provides per-field validators returning human-readable messages,
// plus a small accumulator (`Errors`) that handlers use to collect every failed rule before responding.
// Empty string means "valid".
//
// The helpers each return only the FIRST violated rule for that field so the response stays focused —
// clients get one message per field, not a chain of overlapping ones. Order matters inside each helper:
// more fundamental rules (required, length) before downstream ones (format, charset).
package validation

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
)

const (
	UsernameMinLen = 3
	UsernameMaxLen = 50
	EmailMaxLen    = 254
	PasswordMinLen = 8
	TitleMinLen    = 3
	TitleMaxLen    = 200
	MarkdownMinLen = 10
	NameMinLen     = 1
	NameMaxLen     = 50
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Errors collects per-field validation messages keyed by JSON field name.
// First write wins.
type Errors map[string]string

func New() Errors {
	return Errors{}
}

// Add records `message` under `field` if both are non-empty and no message has been recorded for that field yet.
func (e Errors) Add(field, message string) {
	if message == "" {
		return
	}

	if _, exists := e[field]; exists {
		return
	}

	e[field] = message
}

func (e Errors) IsEmpty() bool {
	return len(e) == 0
}

// Username: required, [3,50] chars, [A-Za-z0-9_-]+. Trimmed.
func ValidateUsername(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "is required"
	case len(s) < UsernameMinLen:
		return "must be at least 3 characters"
	case len(s) > UsernameMaxLen:
		return "must be at most 50 characters"
	case !usernamePattern.MatchString(s):
		return "may only contain letters, digits, '-' and '_'"
	}

	return ""
}

// Email: required, ≤254 chars, RFC-parseable. Trimmed.
func ValidateEmail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "is required"
	}

	if len(s) > EmailMaxLen {
		return "must be at most 254 characters"
	}

	if _, err := mail.ParseAddress(s); err != nil {
		return "is not a valid email"
	}

	return ""
}

// Password: required, ≥8 chars. trimmed.
func ValidatePassword(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "is required"
	}

	if len(s) < PasswordMinLen {
		return fmt.Sprintf("must be at least %d characters", PasswordMinLen)
	}

	return ""
}

// PasswordOptional: same length rule, but the caller is responsible for
// gating this on the field being present (e.g. `if req.Password != nil`).
// Empty string here is treated as a length violation, not "missing".
func PasswordOptional(s string) string {
	if len(s) < PasswordMinLen {
		return "must be at least 8 characters"
	}

	return ""
}

// Title: required, [min: 3, max: 200] chars. Trimmed.
func Title(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "is required"
	case len(s) < TitleMinLen:
		return "must be at least 3 characters"
	case len(s) > TitleMaxLen:
		return "must be at most 200 characters"
	}

	return ""
}

// MarkdownContent: required, ≥10 chars after trim.
func MarkdownContent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "is required"
	}

	if len(s) < MarkdownMinLen {
		return "must be at least 10 characters"
	}

	return ""
}

// URL: required, parses, scheme is http or https, host is present. Trimmed.
func URL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "is required"
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "is not a valid URL"
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return "must use http or https"
	}

	if u.Host == "" {
		return "must include a host"
	}
	return ""
}

// Name: required, [1,50] chars after trim. Unicode-friendly — accepts "AI", "общи теми", "Front-end".
func Name(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "is required"
	case len(s) < NameMinLen:
		return "must be at least 1 character"
	case len(s) > NameMaxLen:
		return "must be at most 50 characters"
	}

	return ""
}
