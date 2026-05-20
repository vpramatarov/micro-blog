package auth_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	authService "github.com/vpramatarov/micro-blog/internal/api/handlers/auth"
	categoriesService "github.com/vpramatarov/micro-blog/internal/api/handlers/categories"
	docsService "github.com/vpramatarov/micro-blog/internal/api/handlers/docs"
	postService "github.com/vpramatarov/micro-blog/internal/api/handlers/posts"
	shortLinksService "github.com/vpramatarov/micro-blog/internal/api/handlers/shortlinks"
	userService "github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	categoriessrepo "github.com/vpramatarov/micro-blog/internal/api/repository/categories"
	postsRepo "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	rbacRepo "github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	shortLinksRepo "github.com/vpramatarov/micro-blog/internal/api/repository/shortlinks"
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

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := testutil.SetupTestDB(t)
	usersRepo := usersRepo.New(db)
	tokensRepo := tokensRepo.New(db)
	rbacRepo := rbacRepo.New(db)
	postsRepo := postsRepo.New(db)
	slRepo := shortLinksRepo.New(db)
	categoriesRepo := categoriessrepo.New(db)

	cfg := &config.Config{
		JWTSecret:     "test-secret",
		JWTAccessTTL:  5 * time.Minute,
		JWTRefreshTTL: time.Hour,
		CookieSecure:  false,
	}

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, auth.IssuerOptions{})
	authSrvc := authService.New(cfg, usersRepo, tokensRepo, issuer, nil)
	usersSrvc := userService.New(cfg, usersRepo, rbacRepo, nil)
	shortLinksSrvc := shortLinksService.New(slRepo, nil, nil)
	categoriesSrvc := categoriesService.New(categoriesRepo, nil)
	postsSrvc := postService.New(postsRepo, categoriesRepo, nil, nil, nil, nil, nil)
	docsSrvc := docsService.New(issuer, nil)
	r := router.New(
		router.Services{Auth: authSrvc, Users: usersSrvc, Posts: postsSrvc, Categories: categoriesSrvc, ShortLinks: shortLinksSrvc, Docs: docsSrvc},
		router.Middlewares{},
	)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func postJSON(t *testing.T, srv *httptest.Server, path string, body any, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	return res
}

func TestRegisterLoginRefreshLogoutFlow(t *testing.T) {
	srv := newTestServer(t)

	// Register
	res := postJSON(t, srv, "/auth/register", map[string]string{
		"username": "alice",
		"email":    "alice@example.com",
		"password": "hunter2hunter2",
	})
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register: got %d, want 201", res.StatusCode)
	}

	// Login
	res = postJSON(t, srv, "/auth/login", map[string]string{
		"email":    "alice@example.com",
		"password": "hunter2hunter2",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login: got %d, want 200", res.StatusCode)
	}

	var loginBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&loginBody); err != nil {
		t.Fatalf("decode login: %v", err)
	}

	res.Body.Close()
	if loginBody.AccessToken == "" {
		t.Fatal("login returned empty access_token")
	}

	loginCookie := findCookie(res.Cookies(), "refresh_token")
	if loginCookie == nil || loginCookie.Value == "" {
		t.Fatal("login did not set refresh_token cookie")
	}

	// Refresh — should rotate, returning a different cookie value.
	res = postJSON(t, srv, "/auth/refresh", struct{}{}, loginCookie)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("refresh: got %d, want 200", res.StatusCode)
	}

	refreshCookie := findCookie(res.Cookies(), "refresh_token")
	res.Body.Close()
	if refreshCookie == nil || refreshCookie.Value == "" || refreshCookie.Value == loginCookie.Value {
		t.Fatalf("refresh did not rotate cookie; old=%v new=%v", loginCookie, refreshCookie)
	}

	// Replay the old cookie -> 401 (proof of rotation).
	res = postJSON(t, srv, "/auth/refresh", struct{}{}, loginCookie)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("replayed old refresh: got %d, want 401", res.StatusCode)
	}

	// Logout with the new cookie.
	res = postJSON(t, srv, "/auth/logout", struct{}{}, refreshCookie)
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("logout: got %d, want 204", res.StatusCode)
	}

	// Refresh after logout -> 401.
	res = postJSON(t, srv, "/auth/refresh", struct{}{}, refreshCookie)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("refresh after logout: got %d, want 401", res.StatusCode)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	srv := newTestServer(t)
	body := map[string]string{
		"username": "bob",
		"email":    "bob@example.com",
		"password": "supersecret123",
	}
	res := postJSON(t, srv, "/auth/register", body)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("first register: got %d, want 201", res.StatusCode)
	}

	body["username"] = "bob2"
	res = postJSON(t, srv, "/auth/register", body)
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate register: got %d, want 409", res.StatusCode)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	srv := newTestServer(t)
	postJSON(t, srv, "/auth/register", map[string]string{
		"username": "carol", "email": "carol@example.com", "password": "rightpassword123",
	}).Body.Close()

	res := postJSON(t, srv, "/auth/login", map[string]string{
		"email": "carol@example.com", "password": "wrongpassword",
	})
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password: got %d, want 401", res.StatusCode)
	}
}

func TestRegisterShortPassword(t *testing.T) {
	srv := newTestServer(t)
	res := postJSON(t, srv, "/auth/register", map[string]string{
		"username": "x", "email": "x@example.com", "password": "short",
	})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("short password: got %d, want 400", res.StatusCode)
	}
}

func TestRegisterMalformedBody(t *testing.T) {
	srv := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/register", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}

	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body: got %d, want 400", res.StatusCode)
	}
}

// TestRegisterValidationEnvelope confirms /auth/register accumulates every failed field into one response.
func TestRegisterValidationEnvelope(t *testing.T) {
	srv := newTestServer(t)
	body := `{"username":"ab","email":"not-an-email","password":"short"}`
	rec := postJSON(t, srv, "/auth/register", json.RawMessage(body))
	if rec.StatusCode != http.StatusBadRequest {
		rec.Body.Close()
		t.Fatalf("status: got %d, want 400", rec.StatusCode)
	}

	assertValidationFields(t, readBody(t, rec.Body), map[string]string{
		"username": "must be at least 3 characters",
		"email":    "is not a valid email",
		"password": "must be at least 8 characters",
	})
}

func TestRegisterValidationMissingFields(t *testing.T) {
	srv := newTestServer(t)
	rec := postJSON(t, srv, "/auth/register", json.RawMessage(`{}`))
	if rec.StatusCode != http.StatusBadRequest {
		rec.Body.Close()
		t.Fatalf("status: got %d, want 400", rec.StatusCode)
	}

	assertValidationFields(t, readBody(t, rec.Body), map[string]string{
		"username": "is required",
		"email":    "is required",
		"password": "is required",
	})
}

func findCookie(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}

	return nil
}

type validationResp struct {
	Error   string            `json:"error"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields"`
}

func decodeValidationResp(t *testing.T, body []byte) validationResp {
	t.Helper()
	var v validationResp
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode validation response: %v; body=%s", err, string(body))
	}
	return v
}

func assertValidationFields(t *testing.T, body []byte, want map[string]string) {
	t.Helper()
	v := decodeValidationResp(t, body)
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

func readBody(t *testing.T, body io.ReadCloser) []byte {
	t.Helper()
	defer body.Close()
	b, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}
