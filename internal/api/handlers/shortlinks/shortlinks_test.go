package shortlinks_test

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

	authh "github.com/vpramatarov/micro-blog/internal/api/handlers/auth"
	categoriesh "github.com/vpramatarov/micro-blog/internal/api/handlers/categories"
	docsh "github.com/vpramatarov/micro-blog/internal/api/handlers/docs"
	postsh "github.com/vpramatarov/micro-blog/internal/api/handlers/posts"
	shortlinksh "github.com/vpramatarov/micro-blog/internal/api/handlers/shortlinks"
	usersh "github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	authmw "github.com/vpramatarov/micro-blog/internal/api/middleware/auth"
	rbacmw "github.com/vpramatarov/micro-blog/internal/api/middleware/rbac"
	categoriessrepo "github.com/vpramatarov/micro-blog/internal/api/repository/categories"
	postsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	rbacrepo "github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	shortlinksrepo "github.com/vpramatarov/micro-blog/internal/api/repository/shortlinks"
	tokensrepo "github.com/vpramatarov/micro-blog/internal/api/repository/tokens"
	usersrepo "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/api/router"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
	"github.com/vpramatarov/micro-blog/internal/shortcode"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

type shortLinkEnv struct {
	srv     http.Handler
	repo    *shortlinksrepo.Repo
	issuer  *auth.Issuer
	encoder *shortcode.Encoder
	tokens  map[string]string
	userID  map[string]int64
}

func setupShortLinkEnv(t *testing.T) *shortLinkEnv {
	t.Helper()
	db := testutil.SetupTestDB(t)
	usersRepo := usersrepo.New(db)
	tokensRepo := tokensrepo.New(db)
	rbacRepo := rbacrepo.New(db)
	postsRepo := postsrepo.New(db)
	shortLinksRepo := shortlinksrepo.New(db)
	categoriesRepo := categoriessrepo.New(db)
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
	encoder, _ := shortcode.New()
	authSvc := authh.New(cfg, usersRepo, tokensRepo, issuer, nil)
	usersSvc := usersh.New(cfg, usersRepo, rbacRepo, nil)
	categoriesSvc := categoriesh.New(categoriesRepo, nil)
	postsSvc := postsh.New(postsRepo, categoriesRepo, nil, nil, nil, encoder, nil)
	shortlinksSvc := shortlinksh.New(shortLinksRepo, encoder, nil)
	docsSvc := docsh.New(issuer, nil)
	r := router.New(
		router.Services{Auth: authSvc, Users: usersSvc, Posts: postsSvc, Categories: categoriesSvc, ShortLinks: shortlinksSvc, Docs: docsSvc},
		router.Middlewares{
			Auth:         authmw.Authenticate(issuer, nil),
			Bouncer:      rbacmw.Bouncer(rbacRepo, postsRepo, shortLinksRepo, nil),
			RequireAdmin: rbacmw.RequireRole("Admin", nil),
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

	return &shortLinkEnv{srv: r, repo: shortLinksRepo, issuer: issuer, encoder: encoder, tokens: tokens, userID: userID}
}

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

func TestCreateShortLinkRoles(t *testing.T) {
	env := setupShortLinkEnv(t)
	body := `{"original_url":"https://example.com/foo?bar=baz"}`

	cases := []struct {
		role string
		want int
	}{
		{"Admin", http.StatusCreated},
		{"Editor", http.StatusCreated},
		{"Author", http.StatusCreated},
		{"Subscriber", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.role, func(t *testing.T) {
			rec := doJSON(t, env.srv, http.MethodPost, "/api/shortlinks", env.tokens[c.role], body)
			if rec.Code != c.want {
				t.Errorf("got %d, want %d; body=%s", rec.Code, c.want, rec.Body.String())
			}
		})
	}

	// No token -> 401.
	rec := doJSON(t, env.srv, http.MethodPost, "/api/shortlinks", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
}

func TestCreateShortLinkResponseShape(t *testing.T) {
	env := setupShortLinkEnv(t)
	body := `{"original_url":"https://example.com/path"}`
	rec := doJSON(t, env.srv, http.MethodPost, "/api/shortlinks", env.tokens["Author"], body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var got shortlinksrepo.ShortLink
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.ID == 0 {
		t.Error("zero id returned")
	}

	if got.UserID != env.userID["Author"] {
		t.Errorf("user_id: got %d, want %d", got.UserID, env.userID["Author"])
	}

	if got.OriginalURL != "https://example.com/path" {
		t.Errorf("original_url mismatch: %q", got.OriginalURL)
	}

	if got.Code == "" {
		t.Error("response missing hashid code")
	}

	// Round-trip the code through the encoder.
	decoded, err := env.encoder.Decode(got.Code)
	if err != nil || decoded != got.ID {
		t.Errorf("code did not round-trip: decoded=%d, err=%v, want id=%d", decoded, err, got.ID)
	}
}

func TestCreateShortLinkValidation(t *testing.T) {
	env := setupShortLinkEnv(t)
	cases := []struct {
		name, body string
	}{
		{"malformed json", "not json"},
		{"missing url", `{}`},
		{"empty url", `{"original_url":""}`},
		{"whitespace url", `{"original_url":"   "}`},
		{"no scheme", `{"original_url":"example.com"}`},
		{"ftp scheme", `{"original_url":"ftp://example.com/x"}`},
		{"javascript scheme", `{"original_url":"javascript:alert(1)"}`},
		{"missing host", `{"original_url":"http://"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doJSON(t, env.srv, http.MethodPost, "/api/shortlinks", env.tokens["Admin"], c.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("got %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestListShortLinksFiltering(t *testing.T) {
	env := setupShortLinkEnv(t)
	ctx := t.Context()

	mustCreate := func(userID int64, url string) {
		t.Helper()
		if _, err := env.repo.Create(ctx, userID, url); err != nil {
			t.Fatalf("seed %s: %v", url, err)
		}
	}
	mustCreate(env.userID["Author"], "https://example.com/a1")
	mustCreate(env.userID["Author"], "https://example.com/a2")
	mustCreate(env.userID["Editor"], "https://example.com/e1")

	cases := []struct {
		role string
		want int
	}{
		{"Admin", 3},      // sees all
		{"Editor", 1},     // own only
		{"Author", 2},     // own only
		{"Subscriber", 0}, // owns none
	}
	for _, c := range cases {
		t.Run(c.role, func(t *testing.T) {
			rec := doJSON(t, env.srv, http.MethodGet, "/api/shortlinks", env.tokens[c.role], "")
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d, want 200; body=%s", rec.Code, rec.Body.String())
			}

			var page struct {
				Items []shortlinksrepo.ShortLink `json:"items"`
				Total int                        `json:"total"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatalf("decode: %v", err)
			}

			if len(page.Items) != c.want || page.Total != c.want {
				t.Errorf("%s: got %d items / total=%d, want %d", c.role, len(page.Items), page.Total, c.want)
			}

			// Every link must carry a hashid in the response.
			for _, l := range page.Items {
				if l.Code == "" {
					t.Errorf("%s: link %d missing code", c.role, l.ID)
				}
			}
		})
	}
}

func TestUpdateShortLinkOwnership(t *testing.T) {
	env := setupShortLinkEnv(t)
	ctx := t.Context()

	authorLink, err := env.repo.Create(ctx, env.userID["Author"], "https://example.com/a")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := `{"original_url":"https://example.com/changed"}`

	// Author own -> 200.
	rec := doJSON(t, env.srv, http.MethodPut, fmt.Sprintf("/api/shortlinks/%d", authorLink), env.tokens["Author"], body)
	if rec.Code != http.StatusOK {
		t.Errorf("author own: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Editor other's -> 403 (scope='own' for shortlink:edit).
	rec = doJSON(t, env.srv, http.MethodPut, fmt.Sprintf("/api/shortlinks/%d", authorLink), env.tokens["Editor"], body)
	if rec.Code != http.StatusForbidden {
		t.Errorf("editor foreign: got %d, want 403", rec.Code)
	}

	// Admin -> 200 regardless of owner.
	rec = doJSON(t, env.srv, http.MethodPut, fmt.Sprintf("/api/shortlinks/%d", authorLink), env.tokens["Admin"], body)
	if rec.Code != http.StatusOK {
		t.Errorf("admin foreign: got %d, want 200", rec.Code)
	}

	// Subscriber -> 403 (no permission at all).
	rec = doJSON(t, env.srv, http.MethodPut, fmt.Sprintf("/api/shortlinks/%d", authorLink), env.tokens["Subscriber"], body)
	if rec.Code != http.StatusForbidden {
		t.Errorf("subscriber: got %d, want 403", rec.Code)
	}

	// Persisted value is the most recent successful update.
	persisted, _ := env.repo.Get(ctx, authorLink)
	if persisted.OriginalURL != "https://example.com/changed" {
		t.Errorf("persisted url: got %q", persisted.OriginalURL)
	}
}

func TestUpdateShortLinkValidation(t *testing.T) {
	env := setupShortLinkEnv(t)
	link, _ := env.repo.Create(t.Context(), env.userID["Admin"], "https://example.com/a")

	rec := doJSON(t, env.srv, http.MethodPut, fmt.Sprintf("/api/shortlinks/%d", link),
		env.tokens["Admin"], `{"original_url":"not-a-url"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad url: got %d, want 400", rec.Code)
	}

	// Admin updating a missing row -> 404 (bouncer bypasses ownership for Admin).
	rec = doJSON(t, env.srv, http.MethodPut, "/api/shortlinks/99999",
		env.tokens["Admin"], `{"original_url":"https://example.com/x"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing: got %d, want 404", rec.Code)
	}
}

func TestDeleteShortLinkOwnership(t *testing.T) {
	env := setupShortLinkEnv(t)
	ctx := t.Context()

	id, err := env.repo.Create(ctx, env.userID["Author"], "https://example.com/a")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	path := fmt.Sprintf("/api/shortlinks/%d", id)
	// Editor (own='own' so foreign -> 403).
	rec := doJSON(t, env.srv, http.MethodDelete, path, env.tokens["Editor"], "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("editor foreign delete: got %d, want 403", rec.Code)
	}

	// Author owns it -> 204.
	rec = doJSON(t, env.srv, http.MethodDelete, path, env.tokens["Author"], "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("author own delete: got %d, want 204", rec.Code)
	}

	if _, err := env.repo.Get(ctx, id); !errors.Is(err, shortlinksrepo.ErrShortLinkNotFound) {
		t.Errorf("expected ErrShortLinkNotFound, got %v", err)
	}

	// Re-delete -> bouncer's OwnerLookup returns ErrShortLinkNotFound which it treats as a denial -> 403 for the Author (no scope='all').
	// Admin would get a 404 because their scope='all' bypasses OwnerLookup and the handler then sees the missing row.
	rec = doJSON(t, env.srv, http.MethodDelete, path, env.tokens["Author"], "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("re-delete as author: got %d, want 403", rec.Code)
	}

	rec = doJSON(t, env.srv, http.MethodDelete, path, env.tokens["Admin"], "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("re-delete as admin: got %d, want 404", rec.Code)
	}
}

func TestResolveShortLinkPublic(t *testing.T) {
	env := setupShortLinkEnv(t)
	ctx := t.Context()

	id, err := env.repo.Create(ctx, env.userID["Admin"], "https://example.com/destination")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	code, err := env.encoder.Encode(id)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Happy path — unauthenticated request, valid code.
	rec := doJSON(t, env.srv, http.MethodGet, "/s/"+code, "", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("resolve: got %d, want 302; body=%s", rec.Code, rec.Body.String())
	}

	if loc := rec.Header().Get("Location"); loc != "https://example.com/destination" {
		t.Errorf("Location: got %q, want %q", loc, "https://example.com/destination")
	}

	// Bad code -> 404.
	rec = doJSON(t, env.srv, http.MethodGet, "/s/!!!", "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("bad code: got %d, want 404", rec.Code)
	}

	// Valid-shape code that decodes to a non-existent id -> 404.
	missingCode, _ := env.encoder.Encode(99999)
	rec = doJSON(t, env.srv, http.MethodGet, "/s/"+missingCode, "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing id: got %d, want 404", rec.Code)
	}
}

func TestResolveShortLinkAfterUpdate(t *testing.T) {
	env := setupShortLinkEnv(t)
	ctx := t.Context()

	id, _ := env.repo.Create(ctx, env.userID["Author"], "https://example.com/v1")
	code, _ := env.encoder.Encode(id)

	// PUT changes the original_url; the next resolve must reflect the change (302 is used precisely so this works for clients).
	body := `{"original_url":"https://example.com/v2"}`
	rec := doJSON(t, env.srv, http.MethodPut, fmt.Sprintf("/api/shortlinks/%d", id), env.tokens["Author"], body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d, want 200", rec.Code)
	}

	rec = doJSON(t, env.srv, http.MethodGet, "/s/"+code, "", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("resolve: got %d, want 302", rec.Code)
	}

	if loc := rec.Header().Get("Location"); loc != "https://example.com/v2" {
		t.Errorf("Location after update: got %q, want %q", loc, "https://example.com/v2")
	}
}

// TestCreateShortLinkValidationEnvelope confirms the single-field URL error uses the new envelope.
func TestCreateShortLinkValidationEnvelope(t *testing.T) {
	env := setupShortLinkEnv(t)
	rec := doJSON(t, env.srv, http.MethodPost, "/api/shortlinks", env.tokens["Admin"],
		`{"original_url":"ftp://example.com/x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}

	assertValidationFields(t, rec.Body.Bytes(), map[string]string{
		"original_url": "must use http or https",
	})
}

// TestResolveShortLinkExternalStateTemplate confirms that a target whose host differs from request's host triggers the click-through warning instead of a 302.
func TestResolveShortLinkExternalStateTemplate(t *testing.T) {
	env := setupShortLinkEnv(t)
	ctx := t.Context()
	id, err := env.repo.Create(ctx, env.userID["Admin"], "https://attacker.example.org/phishing?q=1")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	code, _ := env.encoder.Encode(id)
	rec := doJSON(t, env.srv, http.MethodGet, "/s/"+code, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status template: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type: got %q, want text/html prefix", ct)
	}

	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store", cc)
	}

	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("Location: got %q, want empty (no redirect on state template)", loc)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "attaker.example.org") {
		t.Errorf("state template body missing destination host; body=%s", body)
	}

	if !strings.Contains(body, `rel="noopener noreferrer"`) {
		t.Errorf("state template body missing rel=noopener noreferrer; body=%s", body)
	}

	if !strings.Contains(body, "https://attacker.example.org/phishing?q=1") {
		t.Errorf("state template body missing full URL; body=%s", body)
	}
}

// TestResolveShortLinkSameHostStillRedirects pins the same-origin shortcut: when the target host matches r.Host, the 302 behavior is preserved.
func TestResolveShortLinkSameHostStillRedirects(t *testing.T) {
	env := setupShortLinkEnv(t)
	ctx := t.Context()

	id, err := env.repo.Create(ctx, env.userID["Admin"], "https://example.com/internal")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	code, _ := env.encoder.Encode(id)
	rec := doJSON(t, env.srv, http.MethodGet, "/s/"+code, "", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("same-host: got %d, want 302; body=%s", rec.Code, rec.Body.String())
	}

	if loc := rec.Header().Get("Location"); loc != "https://example.com/internal" {
		t.Errorf("Location: got %q, want https://example.com/internal", loc)
	}
}

func assertValidationFields(t *testing.T, body []byte, want map[string]string) {
	t.Helper()
	type validationResp struct {
		Error   string            `json:"error"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	}
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
