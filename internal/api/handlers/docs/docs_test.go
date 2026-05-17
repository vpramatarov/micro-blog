package docs_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authService "github.com/vpramatarov/micro-blog/internal/api/handlers/auth"
	docService "github.com/vpramatarov/micro-blog/internal/api/handlers/docs"
	userService "github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	"github.com/vpramatarov/micro-blog/internal/api/router"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
)

// buildDocsRouter constructs a real chi mux with nil deps for everything that
// isn't exercised by the docs tests. /openapi.* and /docs only need the docs
// service (which only needs the Issuer).
func buildDocsRouter(t *testing.T, issuer *auth.Issuer) http.Handler {
	t.Helper()
	cfg := &config.Config{}
	authSrvc := authService.New(cfg, nil, nil, nil, nil)
	usersSrvc := userService.New(cfg, nil, nil, nil)
	docsSrvc := docService.New(issuer, nil)
	return router.New(
		router.Services{Auth: authSrvc, Users: usersSrvc, Docs: docsSrvc},
		router.Middlewares{},
	)
}

// TestOpenAPIJSONFilteredByBearerToken hits /openapi.json with every
// audience's token (and once with no token) and verifies the served spec
// only contains the operations that audience can see. End-to-end check of
// the handler -> audienceFor -> pre-filtered map plumbing.
func TestOpenAPIJSONFilteredByBearerToken(t *testing.T) {
	cfg := &config.Config{JWTSecret: "test", JWTAccessTTL: 5 * time.Minute, JWTRefreshTTL: time.Hour}
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, auth.IssuerOptions{})
	r := buildDocsRouter(t, issuer)

	tokenFor := func(role string, roleID int64) string {
		t.Helper()
		tok, err := issuer.Access(auth.UserClaim{UserID: 1, Email: "x", Role: role, RoleID: roleID})
		if err != nil {
			t.Fatalf("issue %s token: %v", role, err)
		}

		return tok
	}

	cases := []struct {
		name        string
		bearer      string
		mustHave    []string
		mustNotHave []string
	}{
		{
			name:        "anonymous (no token)",
			bearer:      "",
			mustHave:    []string{"/posts", "/auth/login"},
			mustNotHave: []string{"/api/me", "/admin/posts", "/admin/users", "/admin/post/{id}"},
		},
		{
			name:        "subscriber",
			bearer:      tokenFor("Subscriber", 4),
			mustHave:    []string{"/api/me", "/api/shortlinks", "/admin/posts"},
			mustNotHave: []string{"/admin/users", "/admin/post/{id}"},
		},
		{
			name:        "author",
			bearer:      tokenFor("Author", 3),
			mustHave:    []string{"/api/me", "/api/shortlinks", "/admin/posts", "/admin/posts/{id}"},
			mustNotHave: []string{"/admin/users", "/admin/post/{id}"},
		},
		{
			name:        "editor",
			bearer:      tokenFor("Editor", 2),
			mustHave:    []string{"/api/me", "/api/shortlinks", "/admin/posts", "/admin/posts/{id}"},
			mustNotHave: []string{"/admin/users", "/admin/post/{id}"},
		},
		{
			name:        "admin sees everything",
			bearer:      tokenFor("Admin", 1),
			mustHave:    []string{"/api/me", "/admin/users", "/admin/posts/{id}", "/admin/post/{id}"},
			mustNotHave: nil,
		},
		{
			name:        "expired token falls back to anonymous",
			bearer:      mustExpiredToken(t),
			mustHave:    []string{"/posts", "/auth/login"},
			mustNotHave: []string{"/api/me", "/admin/users"},
		},
		{
			name:        "forged role falls back to anonymous",
			bearer:      tokenFor("RootKing", 99),
			mustHave:    []string{"/posts"},
			mustNotHave: []string{"/api/me", "/admin/users"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200", rec.Code)
			}

			paths := pathsIn(t, rec.Body.Bytes())
			for _, p := range tc.mustHave {
				if !paths[p] {
					t.Errorf("expected path %q in spec for %s", p, tc.name)
				}
			}

			for _, p := range tc.mustNotHave {
				if paths[p] {
					t.Errorf("unexpected path %q present in spec for %s", p, tc.name)
				}
			}
		})
	}
}

func mustExpiredToken(t *testing.T) string {
	t.Helper()
	issuer := auth.NewIssuer("test", -1*time.Second, auth.IssuerOptions{})
	tok, err := issuer.Access(auth.UserClaim{UserID: 1, Email: "x", Role: "Admin", RoleID: 1})
	if err != nil {
		t.Fatalf("issue expired token: %v", err)
	}

	return tok
}

func pathsIn(t *testing.T, raw []byte) map[string]bool {
	t.Helper()
	var doc struct {
		Paths map[string]any `json:"paths"`
	}

	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode spec: %v; body=%s", err, string(raw))
	}

	out := map[string]bool{}
	for p := range doc.Paths {
		out[p] = true
	}

	return out
}

// TestOpenAPIYAMLAlsoFiltered double-checks the YAML endpoint applies the
// same filtering. We only need a smoke check — the same code path picks
// between SpecYAMLByRole and SpecJSONByRole, so behavior is symmetric.
func TestOpenAPIYAMLAlsoFiltered(t *testing.T) {
	cfg := &config.Config{JWTSecret: "test", JWTAccessTTL: 5 * time.Minute, JWTRefreshTTL: time.Hour}
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, auth.IssuerOptions{})
	r := buildDocsRouter(t, issuer)

	req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	// Anonymous: must contain /auth/login (public), must not contain /admin/users.
	if !strings.Contains(body, "/auth/login:") {
		t.Errorf("anonymous yaml missing /auth/login: %q", body[:min(400, len(body))])
	}

	if strings.Contains(body, "/admin/users:") {
		t.Errorf("anonymous yaml leaked /admin/users")
	}
}
