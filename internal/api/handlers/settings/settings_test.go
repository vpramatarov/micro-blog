package settings_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	authh "github.com/vpramatarov/micro-blog/internal/api/handlers/auth"
	categoriesh "github.com/vpramatarov/micro-blog/internal/api/handlers/categories"
	commentsh "github.com/vpramatarov/micro-blog/internal/api/handlers/comments"
	docsh "github.com/vpramatarov/micro-blog/internal/api/handlers/docs"
	postsh "github.com/vpramatarov/micro-blog/internal/api/handlers/posts"
	settingsh "github.com/vpramatarov/micro-blog/internal/api/handlers/settings"
	shortlinksh "github.com/vpramatarov/micro-blog/internal/api/handlers/shortlinks"
	tagsh "github.com/vpramatarov/micro-blog/internal/api/handlers/tags"
	usersh "github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	authmw "github.com/vpramatarov/micro-blog/internal/api/middleware/auth"
	rbacmw "github.com/vpramatarov/micro-blog/internal/api/middleware/rbac"
	settingsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/settings"
	usersrepo "github.com/vpramatarov/micro-blog/internal/api/repository/users"
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

// buildApp wires only what the /admin/settings/* routes need: the auth
// middleware, the RequireEditorOrAdmin gate, and the settings service. The
// other services are constructed empty because their routes aren't exercised.
func buildApp(t *testing.T) (http.Handler, *usersrepo.Repo, *auth.Issuer, *sql.DB) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	usersRepo := usersrepo.New(db)
	settingsRepo := settingsrepo.New(db)

	cfg := &config.Config{JWTSecret: "test", JWTAccessTTL: 5 * time.Minute, JWTRefreshTTL: time.Hour}
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, auth.IssuerOptions{})
	r := router.New(
		router.Services{
			Auth:       authh.New(cfg, nil, nil, nil, nil),
			Users:      usersh.New(cfg, nil, nil, nil),
			Posts:      postsh.New(nil, nil, nil, nil, nil, nil, nil, nil),
			ShortLinks: shortlinksh.New(nil, nil, nil),
			Docs:       docsh.New(issuer, nil),
			Categories: categoriesh.New(nil, nil),
			Tags:       tagsh.New(nil, nil),
			Comments:   commentsh.New(nil, nil, nil, nil),
			Settings:   settingsh.New(settingsRepo, nil),
		},
		router.Middlewares{
			Auth:                 authmw.Authenticate(issuer, nil, nil),
			RequireAdmin:         rbacmw.RequireRole("Admin", nil),
			RequireEditorOrAdmin: rbacmw.RequireAnyRole(nil, "Admin", "Editor"),
		},
	)
	return r, usersRepo, issuer, db
}

func do(t *testing.T, srv http.Handler, method, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/admin/settings/comments", nil)
	} else {
		req = httptest.NewRequest(method, "/admin/settings/comments", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestCommentsSettingRoleGate(t *testing.T) {
	r, usersRepo, issuer, _ := buildApp(t)
	ctx := t.Context()

	issue := func(username, role string, roleID int64) string {
		t.Helper()
		uid, err := usersRepo.Create(ctx, username, username+"@example.com", "h", roleID)
		if err != nil {
			t.Fatalf("create %s: %v", username, err)
		}
		tok, err := issuer.Access(auth.UserClaim{UserID: uid, Email: "x", Role: role, RoleID: roleID})
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		return tok
	}

	cases := []struct {
		who   string
		token string
		want  int
	}{
		{"Admin", issue("admin", "Admin", 1), http.StatusOK},
		{"Editor", issue("editor", "Editor", 2), http.StatusOK},
		{"Author", issue("author", "Author", 3), http.StatusForbidden},
		{"Subscriber", issue("sub", "Subscriber", 4), http.StatusForbidden},
		{"anonymous", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run("GET as "+tc.who, func(t *testing.T) {
			if rec := do(t, r, http.MethodGet, tc.token, ""); rec.Code != tc.want {
				t.Errorf("got %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
		t.Run("PUT as "+tc.who, func(t *testing.T) {
			if rec := do(t, r, http.MethodPut, tc.token, `{"enabled":true}`); rec.Code != tc.want {
				t.Errorf("got %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestCommentsSettingRoundTrip(t *testing.T) {
	r, usersRepo, issuer, _ := buildApp(t)
	ctx := t.Context()

	uid, err := usersRepo.Create(ctx, "editor", "editor@example.com", "h", 2)
	if err != nil {
		t.Fatalf("create editor: %v", err)
	}
	token, err := issuer.Access(auth.UserClaim{UserID: uid, Email: "x", Role: "Editor", RoleID: 2})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	decode := func(rec *httptest.ResponseRecorder) bool {
		t.Helper()
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
		}
		return body.Enabled
	}

	// Seeded default is enabled.
	if got := decode(do(t, r, http.MethodGet, token, "")); !got {
		t.Error("initial state: got disabled, want the seeded enabled")
	}
	// PUT false persists and echoes.
	if got := decode(do(t, r, http.MethodPut, token, `{"enabled":false}`)); got {
		t.Error("PUT false: response still says enabled")
	}
	if got := decode(do(t, r, http.MethodGet, token, "")); got {
		t.Error("GET after PUT false: got enabled, want disabled")
	}
	// And back on.
	if got := decode(do(t, r, http.MethodPut, token, `{"enabled":true}`)); !got {
		t.Error("PUT true: response still says disabled")
	}

	// Malformed body → 400 invalid_body.
	if rec := do(t, r, http.MethodPut, token, `{"enabled":`); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body: got %d, want 400", rec.Code)
	}
}
