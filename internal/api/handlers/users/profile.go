package users

import (
	"net/http"
	"strings"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	usersRepo "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

// profileFields are the parts of a user update that anyone can change on
// their own profile (Admin or self-service) — explicitly does NOT include
// role_id. Used by both users.go (admin partial update) and me.go
// (self-service partial update) so neither path can accidentally leak the
// role-escalation surface.
type profileFields struct {
	Username *string
	Email    *string
	Password *string
}

// buildProfileUpdate validates the common username/email/password fields and
// populates `errs` with any per-field violations. Returns the partial
// UserUpdate. The second return value is false only when an unexpected
// internal error occurred (already written as 500); validation failures do
// NOT short-circuit so callers can collect their own additional errors
// (e.g. role_id) into the same accumulator.
func (s *Service) buildProfileUpdate(w http.ResponseWriter, f profileFields, errs validation.Errors) (usersRepo.UserUpdate, bool) {
	update := usersRepo.UserUpdate{}
	if f.Username != nil {
		if msg := validation.ValidateUsername(*f.Username); msg != "" {
			errs.Add("username", msg)
		} else {
			trimmed := strings.TrimSpace(*f.Username)
			update.Username = &trimmed
		}
	}

	if f.Email != nil {
		if msg := validation.ValidateEmail(*f.Email); msg != "" {
			errs.Add("email", msg)
		} else {
			trimmed := strings.TrimSpace(strings.ToLower(*f.Email))
			update.Email = &trimmed
		}
	}

	if f.Password != nil {
		if msg := validation.PasswordOptional(*f.Password); msg != "" {
			errs.Add("password", msg)
		} else {
			hash, err := auth.Hash(*f.Password)
			if err != nil {
				s.Log.Error("hash password", "err", err)
				httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update profile")
				return update, false
			}

			update.PasswordHash = &hash
		}
	}

	return update, true
}
