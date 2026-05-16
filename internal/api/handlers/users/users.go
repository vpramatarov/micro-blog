// Package users implements the /admin/users/* CRUD endpoints and the
// /api/me self-service profile endpoints. The two co-locate because they
// share the profileFields / buildProfileUpdate helper that keeps role-id
// out of the self-service partial-update surface.
package users

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	"github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	usersRepo "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

// Service handles the user-CRUD and self-service profile endpoints. It needs
// users.Repo for row reads/writes and rbac.Repo for the role_id existence
// check on create / update.
type Service struct {
	Cfg   *config.Config
	Users *usersRepo.Repo
	RBAC  *rbac.Repo
	Log   *slog.Logger
}

func New(cfg *config.Config, usersRepo *usersRepo.Repo, rbacRepo *rbac.Repo, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}

	return &Service{Cfg: cfg, Users: usersRepo, RBAC: rbacRepo, Log: log}
}

// validRoleIDs are the role IDs seeded by migration 00004. Hard-coded here
// (not in the validation package) because the set is tightly coupled to that
// migration and only used by user-CRUD.
var validRoleIDs = map[int64]bool{1: true, 2: true, 3: true, 4: true}

func validateRoleID(id int64) string {
	if id == 0 {
		return "is required"
	}

	if !validRoleIDs[id] {
		return "must be one of 1, 2, 3, 4"
	}

	return ""
}

type userCreateRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	RoleID   int64  `json:"role_id"`
}

type userUpdateRequest struct {
	Username *string `json:"username"`
	Email    *string `json:"email"`
	Password *string `json:"password"`
	RoleID   *int64  `json:"role_id"`
}

// List — GET /admin/users. Admin only. Paginated via ?page / ?per_page.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	limit, offset, page, perPage, ok := httpx.ParsePagination(w, r)
	if !ok {
		return
	}

	total, err := s.Users.Count(r.Context())
	if err != nil {
		s.Log.Error("count users", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list users")
		return
	}

	users, err := s.Users.List(r.Context(), limit, offset)
	if err != nil {
		s.Log.Error("list users", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not list users")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, httpx.Page[usersRepo.User]{
		Items: users, Page: page, PerPage: perPage, Total: total,
	})
}

// GetUser — GET /admin/users/{id}. Admin only.
func (s *Service) GetUser(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid user id")
		return
	}

	user, err := s.Users.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, usersRepo.ErrUserNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}

		s.Log.Error("get user", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load user")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, user)
}

// Create — POST /admin/users. Admin only.
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	var req userCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	errs := validation.New()
	errs.Add("username", validation.ValidateUsername(req.Username))
	errs.Add("email", validation.ValidateEmail(req.Email))
	errs.Add("password", validation.ValidatePassword(req.Password))
	errs.Add("role_id", validateRoleID(req.RoleID))
	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	// Existence check only after the format rules pass — one DB round-trip
	// we don't pay for malformed input.
	exists, err := s.RBAC.RoleExists(r.Context(), req.RoleID)
	if err != nil {
		s.Log.Error("role exists check", "err", err, "role_id", req.RoleID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create user")
		return
	}

	if !exists {
		httpx.WriteValidationError(w, map[string]string{"role_id": "does not exist"})
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	hash, err := auth.Hash(req.Password)
	if err != nil {
		s.Log.Error("hash password", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create user")
		return
	}

	id, err := s.Users.Create(r.Context(), req.Username, req.Email, hash, req.RoleID)
	if err != nil {
		if errors.Is(err, usersRepo.ErrUserDuplicate) {
			httpx.WriteError(w, http.StatusConflict, "duplicate", "username or email already exists")
			return
		}
		s.Log.Error("create user", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create user")
		return
	}

	user, err := s.Users.GetByID(r.Context(), id)
	if err != nil {
		s.Log.Error("load created user", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load user")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, user)
}

// Update — PUT /admin/users/{id}. Admin only. Partial update — any field
// omitted from the body is left as-is. Sending `"password"` re-hashes and
// replaces password_hash; the hash is never echoed in the response.
func (s *Service) Update(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid user id")
		return
	}

	var req userUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	errs := validation.New()
	update, ok := s.buildProfileUpdate(w, profileFields{
		Username: req.Username, Email: req.Email, Password: req.Password,
	}, errs)
	if !ok {
		return // 500 already written
	}

	roleNeedsDBCheck := false
	if req.RoleID != nil {
		errs.Add("role_id", validateRoleID(*req.RoleID))
		roleNeedsDBCheck = errs["role_id"] == ""
	}

	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	if roleNeedsDBCheck {
		exists, err := s.RBAC.RoleExists(r.Context(), *req.RoleID)
		if err != nil {
			s.Log.Error("role exists check", "err", err, "role_id", *req.RoleID)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update user")
			return
		}

		if !exists {
			httpx.WriteValidationError(w, map[string]string{"role_id": "does not exist"})
			return
		}

		update.RoleID = req.RoleID
	}

	if err := s.Users.Update(r.Context(), id, update); err != nil {
		switch {
		case errors.Is(err, usersRepo.ErrUserNotFound):
			httpx.WriteError(w, http.StatusNotFound, "not_found", "user not found")
		case errors.Is(err, usersRepo.ErrUserDuplicate):
			httpx.WriteError(w, http.StatusConflict, "duplicate", "username or email already exists")
		default:
			s.Log.Error("update user", "err", err, "id", id)
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not update user")
		}

		return
	}

	user, err := s.Users.GetByID(r.Context(), id)
	if err != nil {
		s.Log.Error("load updated user", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load user")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, user)
}

// Delete — DELETE /admin/users/{id}. Admin only. Refuses self-delete to
// avoid an Admin locking themselves out.
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseIDParam(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "invalid user id")
		return
	}

	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing auth")
		return
	}

	if claims.UserID == id {
		httpx.WriteError(w, http.StatusBadRequest, "self_delete", "admins cannot delete themselves")
		return
	}

	if err := s.Users.Delete(r.Context(), id); err != nil {
		if errors.Is(err, usersRepo.ErrUserNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}

		s.Log.Error("delete user", "err", err, "id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not delete user")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
