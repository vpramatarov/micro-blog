package users

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	usersrepo "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

// meUpdateRequest is intentionally narrower than userUpdateRequest —
// it has no role_id so a Subscriber can't promote themselves and no Admin can demote themselves through the self-service path.
type meUpdateRequest struct {
	Username *string `json:"username"`
	Email    *string `json:"email"`
	Password *string `json:"password"`
}

// GetMe — GET /api/me. Any authenticated user. Returns the caller's own row.
func (s *Service) GetMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	user, err := s.Users.GetByID(r.Context(), claims.UserID)
	if err != nil {
		if errors.Is(err, usersrepo.ErrUserNotFound) {
			// Token outlived the user — treat as a fresh-auth-needed signal.
			httpx.WriteError(w, http.StatusUnauthorized, "user_not_found", "user no longer exists")
			return
		}

		s.Log.Error("load me", "err", err, "user_id", claims.UserID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load profile")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, user)
}

// UpdateMe — PUT /api/me. Any authenticated user.
// Partial update of username / email / password on the caller's own row.
// role_id is ignored even if supplied so callers cannot escalate.
func (s *Service) UpdateMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	var req meUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	errs := validation.New()
	update, ok2 := s.buildProfileUpdate(w, profileFields{
		Username: req.Username, Email: req.Email, Password: req.Password,
	}, errs)
	if !ok2 {
		return // 500 already written
	}

	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	if err := s.Users.Update(r.Context(), claims.UserID, update); err != nil {
		switch {
		case errors.Is(err, usersrepo.ErrUserNotFound):
			httpx.WriteError(w, http.StatusUnauthorized, "user_not_found", "user no longer exists")
		case errors.Is(err, usersrepo.ErrUserDuplicate):
			httpx.WriteError(w, http.StatusConflict, "duplicate", "username or email already exists")
		default:
			s.Log.Error("update me", "err", err, "user_id", claims.UserID)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update profile")
		}

		return
	}

	user, err := s.Users.GetByID(r.Context(), claims.UserID)
	if err != nil {
		s.Log.Error("load updated me", "err", err, "user_id", claims.UserID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load profile")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, user)
}
