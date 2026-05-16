package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	"github.com/vpramatarov/micro-blog/internal/api/repository/tokens"
	"github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
	"github.com/vpramatarov/micro-blog/internal/validation"
)

const refreshCookieName = "refresh_token"

// refreshCookiePath scopes the refresh-token cookie to /auth/* so the browser
// only sends it to refresh and logout. Tightens CSRF exposure compared to "/"
// without breaking logout (which needs the cookie to revoke the DB row).
const refreshCookiePath = "/auth"

const defaultRegisterRoleID = 4 // Subscriber

// Service handles the /auth/* endpoints. It needs the users repo for the
// register/login row reads, the tokens repo for refresh-token lifecycle, the
// issuer for JWT minting/verifying, and the config for cookie / TTL knobs.
type Service struct {
	Cfg    *config.Config
	Users  *users.Repo
	Tokens *tokens.Repo
	Issuer *auth.Issuer
	Log    *slog.Logger
}

func New(cfg *config.Config, usersRepo *users.Repo, tokensRepo *tokens.Repo, issuer *auth.Issuer, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}

	return &Service{Cfg: cfg, Users: usersRepo, Tokens: tokensRepo, Issuer: issuer, Log: log}
}

type registerRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userView struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     string `json:"role"`
}

type tokenResponse struct {
	AccessToken string   `json:"access_token"`
	User        userView `json:"user"`
}

func (s *Service) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	errs := validation.New()
	errs.Add("username", validation.ValidateUsername(req.Username))
	errs.Add("email", validation.ValidateEmail(req.Email))
	errs.Add("password", validation.ValidatePassword(req.Password))
	if !errs.IsEmpty() {
		httpx.WriteValidationError(w, errs)
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	hash, err := auth.Hash(req.Password)
	if err != nil {
		s.Log.Error("hash password", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not process registration")
		return
	}

	id, err := s.Users.Create(r.Context(), req.Username, req.Email, hash, defaultRegisterRoleID)
	if err != nil {
		if errors.Is(err, users.ErrUserDuplicate) {
			httpx.WriteError(w, http.StatusConflict, "duplicate", "username or email already exists")
			return
		}

		s.Log.Error("create user", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not create user")
		return
	}

	u, err := s.Users.GetByID(r.Context(), id)
	if err != nil {
		s.Log.Error("load created user", "err", err, "user_id", id)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not load user")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, userView{
		ID: u.ID, Username: u.Username, Email: u.Email, Role: u.RoleName,
	})
}

func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_body", "request body is not valid JSON")
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	u, err := s.Users.GetByEmail(r.Context(), req.Email)
	if err != nil {
		// Burn the same bcrypt work the as wrong-password does, so the two take roughly the same wall-clock time —
		// otherwise email existence is observable by timing.
		_ = auth.Verify(auth.DummyHash, req.Password)
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
		return
	}

	if err := auth.Verify(u.PasswordHash, req.Password); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
		return
	}

	access, err := s.Issuer.Access(auth.UserClaim{
		UserID: u.ID, Email: u.Email, Role: u.RoleName, RoleID: u.RoleID,
	})
	if err != nil {
		s.Log.Error("issue access token", "err", err, "user_id", u.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not issue token")
		return
	}

	if err := s.issueRefreshCookie(w, r, u.ID); err != nil {
		s.Log.Error("issue refresh cookie", "err", err, "user_id", u.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not issue refresh token")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, tokenResponse{
		AccessToken: access,
		User: userView{
			ID: u.ID, Username: u.Username, Email: u.Email, Role: u.RoleName,
		},
	})
}

func (s *Service) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil || cookie.Value == "" {
		httpx.WriteError(w, http.StatusUnauthorized, "no_refresh_token", "missing refresh token")
		return
	}

	oldHash := auth.HashRefreshToken(cookie.Value)
	userID, expiresAt, err := s.Tokens.FindOneByHash(r.Context(), oldHash)
	if err != nil || time.Now().After(expiresAt) {
		s.clearRefreshCookie(w)
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
		return
	}

	// Rotate: delete the old row before issuing a new one. Any replay of the old token after this point fails the FindRefreshToken lookup above.
	if err := s.Tokens.Delete(r.Context(), oldHash); err != nil {
		s.Log.Error("delete old refresh token", "err", err, "user_id", userID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not rotate refresh token")
		return
	}

	u, err := s.Users.GetByID(r.Context(), userID)
	if err != nil {
		s.clearRefreshCookie(w)
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_refresh_token", "user no longer exists")
		return
	}

	access, err := s.Issuer.Access(auth.UserClaim{
		UserID: u.ID, Email: u.Email, Role: u.RoleName, RoleID: u.RoleID,
	})
	if err != nil {
		s.Log.Error("issue access token", "err", err, "user_id", u.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not issue token")
		return
	}

	if err := s.issueRefreshCookie(w, r, u.ID); err != nil {
		s.Log.Error("issue refresh cookie", "err", err, "user_id", u.ID)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "could not issue refresh token")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, tokenResponse{
		AccessToken: access,
		User: userView{
			ID: u.ID, Username: u.Username, Email: u.Email, Role: u.RoleName,
		},
	})
}

func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookieName)
	if err == nil && cookie.Value != "" {
		s.Tokens.Delete(r.Context(), auth.HashRefreshToken(cookie.Value))
	}

	s.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) issueRefreshCookie(w http.ResponseWriter, r *http.Request, userID int64) error {
	plain, hash, err := auth.NewRefreshToken()
	if err != nil {
		return err
	}

	expiresAt := time.Now().Add(s.Cfg.JWTRefreshTTL)
	if err := s.Tokens.Insert(r.Context(), userID, hash, expiresAt); err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    plain,
		Path:     refreshCookiePath,
		HttpOnly: true,
		Secure:   s.Cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.Cfg.JWTRefreshTTL.Seconds()),
	})

	return nil
}

func (s *Service) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		HttpOnly: true,
		Secure:   s.Cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}
