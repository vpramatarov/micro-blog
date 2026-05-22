package rbac_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	authMW "github.com/vpramatarov/micro-blog/internal/api/middleware/auth"
	rbacMW "github.com/vpramatarov/micro-blog/internal/api/middleware/rbac"
	"github.com/vpramatarov/micro-blog/internal/auth"
)

func TestRequireRoleAdminAllowed(t *testing.T) {
	srv, issuer := newRoleServer(t)
	tok := mustToken(t, issuer, auth.UserClaim{UserID: 1, Email: "a", Role: "Admin", RoleID: 1})
	if code := roleDo(t, srv, "/admin/ping", tok); code != http.StatusOK {
		t.Errorf("admin: got %d, want 200", code)
	}
}

func TestRequireRoleNonAdminDenied(t *testing.T) {
	srv, issuer := newRoleServer(t)
	for _, c := range []auth.UserClaim{
		{UserID: 2, Email: "b", Role: "Editor", RoleID: 2},
		{UserID: 3, Email: "c", Role: "Author", RoleID: 3},
		{UserID: 4, Email: "d", Role: "Subscriber", RoleID: 4},
	} {
		tok := mustToken(t, issuer, c)
		if code := roleDo(t, srv, "/admin/ping", tok); code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", c.Role, code)
		}
	}
}

func TestRequireRoleNoToken(t *testing.T) {
	srv, _ := newRoleServer(t)
	if code := roleDo(t, srv, "/admin/ping", ""); code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", code)
	}
}

func newRoleServer(t *testing.T) (*httptest.Server, *auth.Issuer) {
	t.Helper()
	issuer := auth.NewIssuer("test-secret", 5*time.Minute, auth.IssuerOptions{})
	r := chi.NewRouter()
	r.Route("/admin", func(r chi.Router) {
		r.Use(authMW.Authenticate(issuer, nil, nil))
		r.Use(rbacMW.RequireRole("Admin", nil))
		r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, issuer
}

func mustToken(t *testing.T, issuer *auth.Issuer, u auth.UserClaim) string {
	t.Helper()
	tok, err := issuer.Access(u)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	return tok
}

func roleDo(t *testing.T, srv *httptest.Server, path, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}

	res.Body.Close()
	return res.StatusCode
}
