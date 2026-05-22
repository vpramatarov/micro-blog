package users_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	authService "github.com/vpramatarov/micro-blog/internal/api/handlers/auth"
	userService "github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	authMW "github.com/vpramatarov/micro-blog/internal/api/middleware/auth"
	rbacMW "github.com/vpramatarov/micro-blog/internal/api/middleware/rbac"
	rbacRepo "github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	tokensRepo "github.com/vpramatarov/micro-blog/internal/api/repository/tokens"
	usersRepo "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/api/router"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// userCrudEnv carries the dependencies the user-CRUD tests need direct access
// to: the repo for inserts/lookups outside the HTTP path, the issuer for
// minting test tokens, and per-role bearer tokens.
type userCrudEnv struct {
	srv    http.Handler
	repo   *usersRepo.Repo
	issuer *auth.Issuer
	tokens map[string]string
	userID map[string]int64
}

func setupUserCrudEnv(t *testing.T) *userCrudEnv {
	t.Helper()
	db := testutil.SetupTestDB(t)
	usersRepo := usersRepo.New(db)
	tokensRepo := tokensRepo.New(db)
	rbacRepo := rbacRepo.New(db)
	ctx := t.Context()

	seed := map[string]struct {
		username string
		email    string
		role     string
		roleID   int64
	}{
		"Admin":      {"admin", "admin@example.com", "Admin", 1},
		"Editor":     {"editor", "editor@example.com", "Editor", 2},
		"Author":     {"author", "author@example.com", "Author", 3},
		"Subscriber": {"sub", "sub@example.com", "Subscriber", 4},
	}
	userID := map[string]int64{}
	for _, u := range seed {
		id, err := usersRepo.Create(ctx, u.username, u.email, "h", u.roleID)
		if err != nil {
			t.Fatalf("create %s: %v", u.role, err)
		}
		userID[u.role] = id
	}

	cfg := &config.Config{JWTSecret: "test", JWTAccessTTL: 5 * time.Minute, JWTRefreshTTL: time.Hour}
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, auth.IssuerOptions{})
	authSvc := authService.New(cfg, usersRepo, tokensRepo, issuer, nil)
	usersSvc := userService.New(cfg, usersRepo, rbacRepo, nil)

	r := router.New(
		router.Services{Auth: authSvc, Users: usersSvc},
		router.Middlewares{
			Auth:         authMW.Authenticate(issuer, nil, nil),
			RequireAdmin: rbacMW.RequireRole("Admin", nil),
		},
	)

	tokens := map[string]string{}
	for role, u := range seed {
		tok, err := issuer.Access(auth.UserClaim{UserID: userID[role], Email: u.email, Role: u.role, RoleID: u.roleID})
		if err != nil {
			t.Fatalf("issue %s token: %v", role, err)
		}
		tokens[role] = tok
	}

	return &userCrudEnv{srv: r, repo: usersRepo, issuer: issuer, tokens: tokens, userID: userID}
}

// doJSON drives a request against the in-memory chi mux. Shared across
// users-handler tests; me_test.go and validation cases here all use it.
func doJSON(t *testing.T, srv http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}

	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func TestUserRoutesRequireAdmin(t *testing.T) {
	env := setupUserCrudEnv(t)

	cases := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/admin/users", ""},
		{http.MethodGet, fmt.Sprintf("/admin/users/%d", env.userID["Author"]), ""},
		{http.MethodPost, "/admin/users", `{"username":"x","email":"x@e.com","password":"xxxxxxxx","role_id":4}`},
		{http.MethodPut, fmt.Sprintf("/admin/users/%d", env.userID["Author"]), `{"username":"renamed"}`},
		{http.MethodDelete, fmt.Sprintf("/admin/users/%d", env.userID["Author"]), ""},
	}

	for _, role := range []string{"Editor", "Author", "Subscriber"} {
		role := role
		t.Run(role, func(t *testing.T) {
			for _, c := range cases {
				rec := doJSON(t, env.srv, c.method, c.path, env.tokens[role], c.body)
				if rec.Code != http.StatusForbidden {
					t.Errorf("%s %s %s: got %d, want 403", role, c.method, c.path, rec.Code)
				}
			}
		})
	}

	// No token at all → 401 on every route.
	for _, c := range cases {
		rec := doJSON(t, env.srv, c.method, c.path, "", c.body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("no token %s %s: got %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

func TestListUsersAdmin(t *testing.T) {
	env := setupUserCrudEnv(t)
	rec := doJSON(t, env.srv, http.MethodGet, "/admin/users", env.tokens["Admin"], "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list users: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var page struct {
		Items   []usersRepo.User `json:"items"`
		Total   int              `json:"total"`
		Page    int              `json:"page"`
		PerPage int              `json:"per_page"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(page.Items) != 4 || page.Total != 4 {
		t.Errorf("got %d items / total=%d, want 4 / 4", len(page.Items), page.Total)
	}

	// password_hash must never appear in the JSON response.
	if strings.Contains(rec.Body.String(), "password_hash") || strings.Contains(rec.Body.String(), `"h"`) {
		t.Errorf("response leaked password_hash: %s", rec.Body.String())
	}
}

func TestListUsersPagination(t *testing.T) {
	env := setupUserCrudEnv(t)

	type page struct {
		Items   []usersRepo.User `json:"items"`
		Total   int              `json:"total"`
		Page    int              `json:"page"`
		PerPage int              `json:"per_page"`
	}

	t.Run("per_page caps how many items return", func(t *testing.T) {
		rec := doJSON(t, env.srv, http.MethodGet, "/admin/users?page=1&per_page=2", env.tokens["Admin"], "")
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200; body=%s", rec.Code, rec.Body.String())
		}

		var p page
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if len(p.Items) != 2 || p.PerPage != 2 || p.Total != 4 {
			t.Errorf("got items=%d per_page=%d total=%d; want 2/2/4", len(p.Items), p.PerPage, p.Total)
		}
	})

	t.Run("page beyond end returns empty items but real total", func(t *testing.T) {
		rec := doJSON(t, env.srv, http.MethodGet, "/admin/users?page=99&per_page=2", env.tokens["Admin"], "")
		var p page
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if len(p.Items) != 0 || p.Total != 4 {
			t.Errorf("got items=%d total=%d; want 0/4", len(p.Items), p.Total)
		}
	})

	t.Run("per_page clamps to MaxPerPage", func(t *testing.T) {
		rec := doJSON(t, env.srv, http.MethodGet, "/admin/users?per_page=99999", env.tokens["Admin"], "")
		var p page
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if p.PerPage != 200 {
			t.Errorf("per_page: got %d, want 200 (MaxPerPage)", p.PerPage)
		}
	})

	t.Run("negative page rejected", func(t *testing.T) {
		rec := doJSON(t, env.srv, http.MethodGet, "/admin/users?page=-1", env.tokens["Admin"], "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("got %d, want 400", rec.Code)
		}
	})

	t.Run("non-numeric per_page rejected", func(t *testing.T) {
		rec := doJSON(t, env.srv, http.MethodGet, "/admin/users?per_page=abc", env.tokens["Admin"], "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("got %d, want 400", rec.Code)
		}
	})
}

func TestGetUserAdmin(t *testing.T) {
	env := setupUserCrudEnv(t)

	rec := doJSON(t, env.srv, http.MethodGet, fmt.Sprintf("/admin/users/%d", env.userID["Author"]), env.tokens["Admin"], "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get user: got %d, want 200", rec.Code)
	}

	var u usersRepo.User
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if u.Username != "author" || u.RoleName != "Author" {
		t.Errorf("unexpected user: %+v", u)
	}

	// 404 for missing user.
	rec = doJSON(t, env.srv, http.MethodGet, "/admin/users/99999", env.tokens["Admin"], "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing: got %d, want 404", rec.Code)
	}
}

func TestCreateUserAdmin(t *testing.T) {
	env := setupUserCrudEnv(t)

	body := `{"username":"newbie","email":"NEW@example.com","password":"supersecure","role_id":3}`
	rec := doJSON(t, env.srv, http.MethodPost, "/admin/users", env.tokens["Admin"], body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var created usersRepo.User
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if created.Username != "newbie" || created.Email != "new@example.com" || created.RoleID != 3 {
		t.Errorf("created mismatch: %+v", created)
	}
	if created.RoleName != "Author" {
		t.Errorf("role name: got %q, want %q", created.RoleName, "Author")
	}

	if strings.Contains(rec.Body.String(), "password") {
		t.Errorf("response leaked password: %s", rec.Body.String())
	}

	// Verify the password actually got hashed (not stored literally).
	persisted, err := env.repo.GetByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("load persisted: %v", err)
	}

	if persisted.PasswordHash == "supersecure" {
		t.Error("password stored as plaintext")
	}

	if err := auth.Verify(persisted.PasswordHash, "supersecure"); err != nil {
		t.Errorf("hash does not verify: %v", err)
	}
}

func TestCreateUserBadInput(t *testing.T) {
	env := setupUserCrudEnv(t)

	cases := []struct {
		name, body string
	}{
		{"malformed", "not json"},
		{"missing username", `{"email":"x@e.com","password":"longpass1","role_id":4}`},
		{"missing email", `{"username":"x","password":"longpass1","role_id":4}`},
		{"short password", `{"username":"x","email":"x@e.com","password":"short","role_id":4}`},
		{"missing role_id", `{"username":"x","email":"x@e.com","password":"longpass1"}`},
		{"invalid role_id", `{"username":"x","email":"x@e.com","password":"longpass1","role_id":99}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doJSON(t, env.srv, http.MethodPost, "/admin/users", env.tokens["Admin"], c.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("got %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCreateUserDuplicate(t *testing.T) {
	env := setupUserCrudEnv(t)
	body := `{"username":"author","email":"unique@example.com","password":"longpass1","role_id":4}`
	rec := doJSON(t, env.srv, http.MethodPost, "/admin/users", env.tokens["Admin"], body)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate username: got %d, want 409", rec.Code)
	}
}

func TestUpdateUserAdmin(t *testing.T) {
	env := setupUserCrudEnv(t)
	path := fmt.Sprintf("/admin/users/%d", env.userID["Author"])

	// Partial — only the role.
	rec := doJSON(t, env.srv, http.MethodPut, path, env.tokens["Admin"], `{"role_id":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("role update: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := env.repo.GetByID(t.Context(), env.userID["Author"])
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if got.RoleID != 2 || got.Username != "author" {
		t.Errorf("partial update altered untouched fields: %+v", got)
	}

	// Change password — confirm hash now verifies the new password.
	rec = doJSON(t, env.srv, http.MethodPut, path, env.tokens["Admin"], `{"password":"brandnew1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("password update: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got, _ = env.repo.GetByID(t.Context(), env.userID["Author"])
	if err := auth.Verify(got.PasswordHash, "brandnew1"); err != nil {
		t.Errorf("new password does not verify: %v", err)
	}
}

func TestUpdateUserConflicts(t *testing.T) {
	env := setupUserCrudEnv(t)
	path := fmt.Sprintf("/admin/users/%d", env.userID["Author"])

	// Renaming Author's email to Editor's email → 409.
	rec := doJSON(t, env.srv, http.MethodPut, path, env.tokens["Admin"], `{"email":"editor@example.com"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("dup email: got %d, want 409", rec.Code)
	}

	// Empty username → 400.
	rec = doJSON(t, env.srv, http.MethodPut, path, env.tokens["Admin"], `{"username":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty username: got %d, want 400", rec.Code)
	}

	// Invalid role → 400.
	rec = doJSON(t, env.srv, http.MethodPut, path, env.tokens["Admin"], `{"role_id":99}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid role: got %d, want 400", rec.Code)
	}

	// Missing user → 404.
	rec = doJSON(t, env.srv, http.MethodPut, "/admin/users/99999", env.tokens["Admin"], `{"username":"validname"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing user: got %d, want 404", rec.Code)
	}
}

func TestDeleteUserAdmin(t *testing.T) {
	env := setupUserCrudEnv(t)
	path := fmt.Sprintf("/admin/users/%d", env.userID["Subscriber"])

	rec := doJSON(t, env.srv, http.MethodDelete, path, env.tokens["Admin"], "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	if _, err := env.repo.GetByID(t.Context(), env.userID["Subscriber"]); !errors.Is(err, usersRepo.ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}

	// Re-delete → 404.
	rec = doJSON(t, env.srv, http.MethodDelete, path, env.tokens["Admin"], "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("re-delete: got %d, want 404", rec.Code)
	}
}

func TestDeleteUserSelfBlocked(t *testing.T) {
	env := setupUserCrudEnv(t)
	path := fmt.Sprintf("/admin/users/%d", env.userID["Admin"])
	rec := doJSON(t, env.srv, http.MethodDelete, path, env.tokens["Admin"], "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("self-delete: got %d, want 400", rec.Code)
	}

	// Admin still exists.
	if _, err := env.repo.GetByID(t.Context(), env.userID["Admin"]); err != nil {
		t.Errorf("admin was deleted: %v", err)
	}
}

// TestCreateUserValidationEnvelope exercises the admin user-create path.
func TestCreateUserValidationEnvelope(t *testing.T) {
	env := setupUserCrudEnv(t)
	rec := doJSON(t, env.srv, http.MethodPost, "/admin/users", env.tokens["Admin"],
		`{"username":"ab","email":"","password":"shrt","role_id":99}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}

	assertValidationFields(t, rec.Body.Bytes(), map[string]string{
		"username": "must be at least 3 characters",
		"email":    "is required",
		"password": "must be at least 8 characters",
		"role_id":  "must be one of 1, 2, 3, 4",
	})
}

// TestUpdateUserValidationEnvelope hits the admin partial-update path with
// every kind of bad field at once.
func TestUpdateUserValidationEnvelope(t *testing.T) {
	env := setupUserCrudEnv(t)
	path := fmt.Sprintf("/admin/users/%d", env.userID["Author"])
	rec := doJSON(t, env.srv, http.MethodPut, path, env.tokens["Admin"],
		`{"username":"   ","email":"bad","password":"abc","role_id":42}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}

	assertValidationFields(t, rec.Body.Bytes(), map[string]string{
		"username": "is required",
		"email":    "is not a valid email",
		"password": "must be at least 8 characters",
		"role_id":  "must be one of 1, 2, 3, 4",
	})
}

type validationResp struct {
	Error   string            `json:"error"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields"`
}

func assertValidationFields(t *testing.T, body []byte, want map[string]string) {
	t.Helper()
	var v validationResp
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode validation response: %v; body=%s", err, string(body))
	}

	if v.Error != "invalid_input" {
		t.Errorf("error code: got %q, want invalid_input", v.Error)
	}

	if v.Message != "validation failed" {
		t.Errorf("message: got %q, want %q", v.Message, "validation failed")
	}

	if !reflect.DeepEqual(v.Fields, want) {
		t.Errorf("fields mismatch:\ngot:  %#v\nwant: %#v", v.Fields, want)
	}
}
