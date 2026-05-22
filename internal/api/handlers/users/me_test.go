package users_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	authService "github.com/vpramatarov/micro-blog/internal/api/handlers/auth"
	usersService "github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	authMW "github.com/vpramatarov/micro-blog/internal/api/middleware/auth"
	rbacMW "github.com/vpramatarov/micro-blog/internal/api/middleware/rbac"

	rbacRepository "github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	tokensRepository "github.com/vpramatarov/micro-blog/internal/api/repository/tokens"
	usersRepository "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/api/router"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

type meEnv struct {
	srv    http.Handler
	repo   *usersRepository.Repo
	issuer *auth.Issuer
	tokens map[string]string
	userID map[string]int64
}

func setupMeEnv(t *testing.T) *meEnv {
	t.Helper()
	db := testutil.SetupTestDB(t)
	usersRepo := usersRepository.New(db)
	tokensRepo := tokensRepository.New(db)
	rbacRepo := rbacRepository.New(db)
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
		// Real bcrypt hash for "originalpw" so the password-change test can re-verify against the persisted hash.
		hash, err := auth.Hash("originalpw")
		if err != nil {
			t.Fatalf("hash: %v", err)
		}

		id, err := usersRepo.Create(ctx, u.username, u.email, hash, u.roleID)
		if err != nil {
			t.Fatalf("create %s: %v", u.role, err)
		}

		userID[u.role] = id
	}

	cfg := &config.Config{JWTSecret: "test", JWTAccessTTL: 5 * time.Minute, JWTRefreshTTL: time.Hour}
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, auth.IssuerOptions{})

	authSrvc := authService.New(cfg, usersRepo, tokensRepo, issuer, nil)
	usersSrvc := usersService.New(cfg, usersRepo, rbacRepo, nil)

	r := router.New(
		router.Services{Auth: authSrvc, Users: usersSrvc},
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

	return &meEnv{srv: r, repo: usersRepo, issuer: issuer, tokens: tokens, userID: userID}
}

func TestGetMeEveryRole(t *testing.T) {
	env := setupMeEnv(t)
	for _, role := range []string{"Admin", "Editor", "Author", "Subscriber"} {
		t.Run(role, func(t *testing.T) {
			rec := doJSON(t, env.srv, http.MethodGet, "/api/me", env.tokens[role], "")
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d, want 200; body=%s", rec.Code, rec.Body.String())
			}

			var u usersRepository.User
			if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
				t.Fatalf("decode: %v", err)
			}

			if u.ID != env.userID[role] || u.RoleName != role {
				t.Errorf("wrong user returned for %s: %+v", role, u)
			}
		})
	}
}

func TestGetMeUnauthenticated(t *testing.T) {
	env := setupMeEnv(t)
	rec := doJSON(t, env.srv, http.MethodGet, "/api/me", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
}

func TestUpdateMeSubscriberChangesProfile(t *testing.T) {
	env := setupMeEnv(t)
	body := `{"username":"new_sub","email":"NEW-SUB@example.com","password":"newsecure1"}`

	rec := doJSON(t, env.srv, http.MethodPut, "/api/me", env.tokens["Subscriber"], body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	persisted, err := env.repo.GetByID(t.Context(), env.userID["Subscriber"])
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if persisted.Username != "new_sub" {
		t.Errorf("username: got %q, want %q", persisted.Username, "new_sub")
	}

	if persisted.Email != "new-sub@example.com" {
		t.Errorf("email: got %q (expected lowercased)", persisted.Email)
	}

	if persisted.RoleID != 4 || persisted.RoleName != "Subscriber" {
		t.Errorf("role changed unexpectedly: %+v", persisted)
	}

	if err := auth.Verify(persisted.PasswordHash, "newsecure1"); err != nil {
		t.Errorf("new password does not verify: %v", err)
	}

	if err := auth.Verify(persisted.PasswordHash, "originalpw"); err == nil {
		t.Error("old password still verifies — hash was not replaced")
	}
}

func TestUpdateMeIgnoresRoleIDInBody(t *testing.T) {
	env := setupMeEnv(t)
	// A Subscriber tries to promote themselves to Admin via the body.
	body := `{"username":"promoted","role_id":1}`

	rec := doJSON(t, env.srv, http.MethodPut, "/api/me", env.tokens["Subscriber"], body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	persisted, _ := env.repo.GetByID(t.Context(), env.userID["Subscriber"])
	if persisted.RoleID != 4 || persisted.RoleName != "Subscriber" {
		t.Fatalf("role escalation via /api/me: %+v", persisted)
	}

	if persisted.Username != "promoted" {
		t.Errorf("username was not applied: %+v", persisted)
	}
}

func TestUpdateMeConflicts(t *testing.T) {
	env := setupMeEnv(t)

	cases := []struct {
		name string
		role string
		body string
		want int
	}{
		{"empty username", "Author", `{"username":"   "}`, http.StatusBadRequest},
		{"empty email", "Author", `{"email":""}`, http.StatusBadRequest},
		{"short password", "Author", `{"password":"short"}`, http.StatusBadRequest},
		{"duplicate email", "Author", `{"email":"editor@example.com"}`, http.StatusConflict},
		{"duplicate username", "Author", `{"username":"admin"}`, http.StatusConflict},
		{"malformed json", "Author", "not json", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doJSON(t, env.srv, http.MethodPut, "/api/me", env.tokens[c.role], c.body)
			if rec.Code != c.want {
				t.Errorf("got %d, want %d; body=%s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

func TestUpdateMeUnauthenticated(t *testing.T) {
	env := setupMeEnv(t)
	rec := doJSON(t, env.srv, http.MethodPut, "/api/me", "", `{"username":"x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
}

func TestMeWhenUserDeleted(t *testing.T) {
	env := setupMeEnv(t)

	// Token issued for the Subscriber, then delete the row out from under it.
	if err := env.repo.Delete(t.Context(), env.userID["Subscriber"]); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// GET /api/me with the now-stale token.
	rec := doJSON(t, env.srv, http.MethodGet, "/api/me", env.tokens["Subscriber"], "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("stale token GET: got %d, want 401", rec.Code)
	}

	// PUT /api/me with the stale token.
	rec = doJSON(t, env.srv, http.MethodPut, "/api/me", env.tokens["Subscriber"], `{"username":"validname"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("stale token PUT: got %d, want 401", rec.Code)
	}
}

// TestUpdateMeValidationEnvelope hits the self-service partial-update path.
func TestUpdateMeValidationEnvelope(t *testing.T) {
	env := setupMeEnv(t)
	rec := doJSON(t, env.srv, http.MethodPut, "/api/me", env.tokens["Subscriber"],
		`{"username":"a","email":"@nope","password":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}

	assertValidationFields(t, rec.Body.Bytes(), map[string]string{
		"username": "must be at least 3 characters",
		"email":    "is not a valid email",
		"password": "must be at least 8 characters",
	})
}
