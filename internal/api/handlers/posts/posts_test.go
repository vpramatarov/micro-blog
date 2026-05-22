package posts_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
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
	tagsh "github.com/vpramatarov/micro-blog/internal/api/handlers/tags"
	usersh "github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	authmw "github.com/vpramatarov/micro-blog/internal/api/middleware/auth"
	rbacmw "github.com/vpramatarov/micro-blog/internal/api/middleware/rbac"
	categoriesrepo "github.com/vpramatarov/micro-blog/internal/api/repository/categories"
	jobsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/jobs"
	postsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	rbacrepo "github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	shortlinksrepo "github.com/vpramatarov/micro-blog/internal/api/repository/shortlinks"
	tagssrepo "github.com/vpramatarov/micro-blog/internal/api/repository/tags"
	tokensrepo "github.com/vpramatarov/micro-blog/internal/api/repository/tokens"
	usersrepo "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/api/router"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
	jobsWorker "github.com/vpramatarov/micro-blog/internal/jobs"
	"github.com/vpramatarov/micro-blog/internal/shortcode"
	"github.com/vpramatarov/micro-blog/internal/testutil"
	"github.com/vpramatarov/micro-blog/internal/uploads"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// buildApp wires the full router with all handler services and middlewares
// against the given DB. Returns the mux plus the repos that tests insert fixture data through.
type appDeps struct {
	r              http.Handler
	usersRepo      *usersrepo.Repo
	postsRepo      *postsrepo.Repo
	categoriesRepo *categoriesrepo.Repo
	tagsRepo       *tagssrepo.Repo
	jobsWorker     *jobsWorker.Worker
	storage        *uploads.Storage
	uploadsRoot    string
	issuer         *auth.Issuer
	encoder        *shortcode.Encoder
}

func buildApp(t *testing.T) (*appDeps, *sql.DB) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	usersRepo := usersrepo.New(db)
	tokensRepo := tokensrepo.New(db)
	rbacRepo := rbacrepo.New(db)
	postsRepo := postsrepo.New(db)
	shortLinksRepo := shortlinksrepo.New(db)
	categoriesRepo := categoriesrepo.New(db)
	tagsRepo := tagssrepo.New(db)
	jobsRepo := jobsrepo.New(db)

	// Per-test sandbox for uploads — wiped automatically by t.Cleanup via t.TempDir,
	// so nothing leaks between tests and we don't touch the project's real ./uploads directory.
	uploadsRoot := t.TempDir()
	storage := uploads.New(uploadsRoot)

	cfg := &config.Config{JWTSecret: "test", JWTAccessTTL: 5 * time.Minute, JWTRefreshTTL: time.Hour, CookieSecure: false}
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, auth.IssuerOptions{})
	encoder, err := shortcode.New()
	if err != nil {
		t.Fatalf("encoder: %v", err)
	}

	authSvc := authh.New(cfg, usersRepo, tokensRepo, issuer, nil)
	usersSvc := usersh.New(cfg, usersRepo, rbacRepo, nil)
	postsSvc := postsh.New(postsRepo, categoriesRepo, tagsRepo, storage, jobsRepo, encoder, nil)
	shortlinksSvc := shortlinksh.New(shortLinksRepo, encoder, nil)
	docsSvc := docsh.New(issuer, nil)
	categoriesSvc := categoriesh.New(categoriesRepo, nil)
	tagsSvc := tagsh.New(tagsRepo, nil)
	jobWorker := jobsWorker.NewWorker(jobsRepo, nil)

	r := router.New(
		router.Services{
			Auth: authSvc, Users: usersSvc, Posts: postsSvc,
			ShortLinks: shortlinksSvc, Docs: docsSvc, Categories: categoriesSvc,
			Tags: tagsSvc, UploadsRoot: uploadsRoot,
		},
		router.Middlewares{
			Auth:                 authmw.Authenticate(issuer, nil, nil),
			Bouncer:              rbacmw.Bouncer(rbacRepo, postsRepo, shortLinksRepo, nil),
			RequireAdmin:         rbacmw.RequireRole("Admin", nil),
			RequireEditorOrAdmin: rbacmw.RequireAnyRole(nil, "Admin", "Editor"),
		},
	)

	return &appDeps{
		r:              r,
		usersRepo:      usersRepo,
		postsRepo:      postsRepo,
		categoriesRepo: categoriesRepo,
		tagsRepo:       tagsRepo,
		storage:        storage,
		jobsWorker:     jobWorker,
		uploadsRoot:    uploadsRoot,
		issuer:         issuer,
		encoder:        encoder,
	}, db
}

// doMultipartPost wraps body (the JSON for the "data" form field) into a multipart request and optionally attaches a file to the "featured_image" field.
// fileBytes==nil → no file. Used by every test that creates or updates a post — the endpoints accept multipart only.
func doMultipartPost(t *testing.T, srv http.Handler, method, path, token, dataJSON string, fileName string, fileBytes []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if dataJSON != "" {
		fw, err := mw.CreateFormField("data")
		if err != nil {
			t.Fatalf("multipart create data field: %v", err)
		}

		if _, err := io.WriteString(fw, dataJSON); err != nil {
			t.Fatalf("multipart write data: %v", err)
		}
	}

	if fileBytes != nil {
		fw, err := mw.CreateFormFile("featured_image", fileName)
		if err != nil {
			t.Fatalf("multipart create file: %v", err)
		}

		if _, err := fw.Write(fileBytes); err != nil {
			t.Fatalf("multipart write file: %v", err)
		}
	}

	if err := mw.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}

	r := httptest.NewRequest(method, path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
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

func TestListPostsAdminFilteringByRole(t *testing.T) {
	app, raw := buildApp(t)
	ctx := t.Context()

	mustUser := func(username, email string, roleID int64) int64 {
		t.Helper()
		id, err := app.usersRepo.Create(ctx, username, email, "h", roleID)
		if err != nil {
			t.Fatalf("create %s: %v", username, err)
		}

		return id
	}
	aliceID := mustUser("alice", "alice@example.com", 3) // Author
	bobID := mustUser("bob", "bob@example.com", 3)       // Author
	carolID := mustUser("carol", "carol@example.com", 1) // Admin
	edID := mustUser("ed", "ed@example.com", 2)          // Editor
	eveID := mustUser("eve", "eve@example.com", 4)       // Subscriber

	mustPost := func(authorID int64, title, slug string) {
		t.Helper()
		if _, err := raw.ExecContext(ctx,
			`INSERT INTO posts (author_id, title, markdown_content, html_content, slug) VALUES (?, ?, '', '', ?)`,
			authorID, title, slug,
		); err != nil {
			t.Fatalf("insert post: %v", err)
		}
	}
	mustPost(aliceID, "alice-1", "alice-1")
	mustPost(aliceID, "alice-2", "alice-2")
	mustPost(bobID, "bob-1", "bob-1")
	// 3 posts total: 2 alice, 1 bob.

	issue := func(uid int64, role string, roleID int64) string {
		tok, err := app.issuer.Access(auth.UserClaim{UserID: uid, Email: "x", Role: role, RoleID: roleID})
		if err != nil {
			t.Fatalf("issue token: %v", err)
		}

		return tok
	}

	tests := []struct {
		who   string
		token string
		want  int
	}{
		{"alice (Author)", issue(aliceID, "Author", 3), 2},
		{"bob (Author)", issue(bobID, "Author", 3), 1},
		{"carol (Admin)", issue(carolID, "Admin", 1), 3},
		{"ed (Editor)", issue(edID, "Editor", 2), 3},
		{"eve (Subscriber)", issue(eveID, "Subscriber", 4), 3},
	}
	for _, tt := range tests {
		t.Run(tt.who, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/posts", nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			rec := httptest.NewRecorder()
			app.r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
			}

			var page struct {
				Items []postsrepo.Post `json:"items"`
				Total int              `json:"total"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
			}

			if len(page.Items) != tt.want || page.Total != tt.want {
				t.Errorf("got %d items / total=%d, want %d / %d", len(page.Items), page.Total, tt.want, tt.want)
			}
		})
	}
}

func TestListPostsAdminUnauthenticated(t *testing.T) {
	app, _ := buildApp(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/posts", nil)
	rec := httptest.NewRecorder()
	app.r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
}

// TestGetPostAdminRequiresAdminRole asserts that /admin/post/{id} (numeric id read) is gated to the Admin role only — Authors/Editors/Subscribers get 403.
func TestGetPostAdminRequiresAdminRole(t *testing.T) {
	app, raw := buildApp(t)
	ctx := t.Context()

	mustUser := func(username, email string, roleID int64) int64 {
		t.Helper()
		id, err := app.usersRepo.Create(ctx, username, email, "h", roleID)
		if err != nil {
			t.Fatalf("create %s: %v", username, err)
		}

		return id
	}
	adminID := mustUser("admin", "admin@example.com", 1)
	editorID := mustUser("editor", "editor@example.com", 2)
	authorID := mustUser("author", "author@example.com", 3)
	subID := mustUser("sub", "sub@example.com", 4)

	res, err := raw.ExecContext(ctx,
		`INSERT INTO posts (author_id, title, markdown_content, html_content, slug) VALUES (?, 't', 'md', '<p>md</p>', 'admin-by-id-post')`,
		authorID,
	)
	if err != nil {
		t.Fatalf("insert post: %v", err)
	}

	postID, _ := res.LastInsertId()
	issue := func(uid int64, role string, roleID int64) string {
		tok, err := app.issuer.Access(auth.UserClaim{UserID: uid, Email: "x", Role: role, RoleID: roleID})
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
		{"admin", issue(adminID, "Admin", 1), http.StatusOK},
		{"editor", issue(editorID, "Editor", 2), http.StatusForbidden},
		{"author", issue(authorID, "Author", 3), http.StatusForbidden},
		{"subscriber", issue(subID, "Subscriber", 4), http.StatusForbidden},
		{"no token", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.who, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/admin/post/%d", postID), nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}

			rec := httptest.NewRecorder()
			app.r.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s: got %d, want %d", tc.who, rec.Code, tc.want)
			}
		})
	}
}

// TestGetPostByHashidPublic asserts the public GET /p/{code} route works
// without auth, decodes the hashid, and returns the right post. A bad code should 404, not leak.
func TestGetPostByHashidPublic(t *testing.T) {
	app, raw := buildApp(t)
	ctx := t.Context()

	authorID, err := app.usersRepo.Create(ctx, "writer", "writer@example.com", "h", 3)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	res, err := raw.ExecContext(ctx,
		`INSERT INTO posts (author_id, title, markdown_content, html_content, slug) VALUES (?, 'hello', '# hi', '<h1>hi</h1>', 'hello-public')`,
		authorID,
	)
	if err != nil {
		t.Fatalf("insert post: %v", err)
	}

	postID, _ := res.LastInsertId()
	code, err := app.encoder.Encode(postID)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Happy path — unauthenticated request, valid code.
	req := httptest.NewRequest(http.MethodGet, "/p/"+code, nil)
	rec := httptest.NewRecorder()
	app.r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("hashid read: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var post postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &post); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}

	if post.ID != postID || post.Title != "hello" {
		t.Errorf("returned wrong post: %+v", post)
	}

	if post.Code != code {
		t.Errorf("code: got %q, want %q", post.Code, code)
	}

	if post.AuthorName != "writer" {
		t.Errorf("author_name: got %q, want %q", post.AuthorName, "writter")
	}

	if post.CategoryName != "Uncategorized" {
		t.Errorf("category_name: got %q, want %q", post.CategoryName, "Uncategorized")
	}

	// Bad code → 404.
	req = httptest.NewRequest(http.MethodGet, "/p/!!!not-a-code", nil)
	rec = httptest.NewRecorder()
	app.r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("bad code: got %d, want 404", rec.Code)
	}

	// Numeric path (would-be raw id) → must NOT be served publicly. With chi's
	// {code} param the numeric string is still matched, but Decode rejects it
	// because sqids returns a different id, and we expect a not-found.
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/p/%d", postID), nil)
	rec = httptest.NewRecorder()
	app.r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		// Decode of a numeric string may either fail or decode to some other id.
		// Either way the response must not be the requested post.
		var p postsrepo.Post
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if p.ID == postID {
			t.Errorf("numeric id leaked the post via public route")
		}
	}
}

// TestGetPostBySlugPublic exercises the new GET /posts/{slug} route. Anonymous readers must see the post with its hydrated category.
func TestGetPostBySlugPublic(t *testing.T) {
	app, _ := buildApp(t)
	ctx := t.Context()

	authorID, err := app.usersRepo.Create(ctx, "writer", "writer@example.com", "h", 3)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	tagA, err := app.tagsRepo.Create(ctx, "go")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}

	tagB, err := app.tagsRepo.Create(ctx, "web")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}

	postID, err := app.postsRepo.Create(ctx, postsrepo.PostInsert{
		AuthorID: authorID, CategoryID: 1, Title: "Hello slug", Slug: "hello-slug", Markdown: "# hi", HTML: "<h1>hi</h1>",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	if err := app.tagsRepo.ReplaceForPost(ctx, postID, []int64{tagA, tagB}); err != nil {
		t.Fatalf("attach tags: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/posts/hello-slug", nil)
	rec := httptest.NewRecorder()
	app.r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("slug read: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got struct {
		ID           int64            `json:"id"`
		Slug         string           `json:"slug"`
		AuthorID     int64            `json:"author_id"`
		AuthorName   string           `json:"author_name"`
		CategoryID   int64            `json:"category_id"`
		CategoryName string           `json:"category_name"`
		Tags         map[int64]string `json:"tags"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}

	if got.ID != postID || got.Slug != "hello-slug" {
		t.Errorf("wrong post: %+v", got)
	}

	if got.AuthorID != authorID || got.AuthorName != "writer" {
		t.Errorf("author: got id=%d name=%q, want id=%d name=writter", got.AuthorID, got.AuthorName, authorID)
	}

	if got.CategoryID != 1 || got.CategoryName != "Uncategorized" {
		t.Errorf("category: got id=%d name=%q, want id=1 name=Uncategorized", got.CategoryID, got.CategoryName)
	}

	if len(got.Tags) != 2 {
		t.Errorf("tags count: got %d, want 2", len(got.Tags))
	}

	if got.Tags[tagA] != "go" || got.Tags[tagB] != "web" {
		t.Errorf("tags map: got %v, want {%d:\"go\", %d:\"web\"}", got.Tags, tagA, tagB)
	}

	// Missing slug → 404.
	req = httptest.NewRequest(http.MethodGet, "/posts/does-not-exist", nil)
	rec = httptest.NewRecorder()
	app.r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing slug: got %d, want 404", rec.Code)
	}
}

// postWriteEnv stands up a real router + auth chain backed by a fresh test DB,
// returning helpers the CRUD tests use to authenticate, hit endpoints, and
// inspect rows directly.
type postWriteEnv struct {
	app    *appDeps
	tokens map[string]string
	userID map[string]int64
}

func setupPostWriteEnv(t *testing.T) *postWriteEnv {
	t.Helper()
	app, _ := buildApp(t)
	ctx := t.Context()

	usersByRole := map[string]struct {
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
	for _, u := range usersByRole {
		id, err := app.usersRepo.Create(ctx, u.username, u.email, "h", u.roleID)
		if err != nil {
			t.Fatalf("create %s: %v", u.role, err)
		}

		userID[u.role] = id
	}

	tokens := map[string]string{}
	for role, u := range usersByRole {
		tok, err := app.issuer.Access(auth.UserClaim{UserID: userID[role], Email: u.email, Role: u.role, RoleID: u.roleID})
		if err != nil {
			t.Fatalf("issue %s token: %v", role, err)
		}

		tokens[role] = tok
	}

	return &postWriteEnv{app: app, tokens: tokens, userID: userID}
}

func TestCreatePostAsAuthor(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title":"hello","markdown_content":"# hi\n\nworld","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Author"], body, "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var got postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}

	if got.ID == 0 {
		t.Error("create returned zero id")
	}

	if got.AuthorID != env.userID["Author"] {
		t.Errorf("author_id: got %d, want %d", got.AuthorID, env.userID["Author"])
	}

	if got.AuthorName != "author" {
		t.Errorf("author_name: got %q, want %q", got.AuthorName, "author")
	}

	if got.CategoryName != "Uncategorized" {
		t.Errorf("category_name: got %q, want %q", got.CategoryName, "Uncategorized")
	}

	if got.Title != "hello" || got.MarkdownContent != "# hi\n\nworld" {
		t.Errorf("content mismatch: %+v", got)
	}

	// HTML content must be rendered, not a copy of the markdown.
	if !strings.Contains(got.HTMLContent, "<h1>hi</h1>") {
		t.Errorf("html_content not rendered as HTML: %q", got.HTMLContent)
	}

	if got.HTMLContent == got.MarkdownContent {
		t.Errorf("html_content equals markdown_content — renderer not applied")
	}

	if got.Code == "" {
		t.Error("response missing hashid code")
	}

	// Verify the row is actually in the DB.
	persisted, err := env.app.postsRepo.GetByID(t.Context(), got.ID)
	if err != nil {
		t.Fatalf("get persisted: %v", err)
	}
	if persisted.Title != "hello" {
		t.Errorf("persisted title: got %q", persisted.Title)
	}
}

func TestCreatePostBadBody(t *testing.T) {
	env := setupPostWriteEnv(t)

	cases := []struct {
		name string
		body string
	}{
		{"malformed", "not json"},
		{"empty title", `{"title":"","markdown_content":"x","category_id":1}`},
		{"whitespace title", `{"title":"   ","markdown_content":"x","category_id":1}`},
		{"empty markdown", `{"title":"t","markdown_content":"","category_id":1}`},
		{"missing category", `{"title":"valid title","markdown_content":"valid markdown body"}`},
		{"unknown category", `{"title":"valid title","markdown_content":"valid markdown body","category_id":999999}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], tc.body, "", nil)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("got %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCreatePostSubscriberDenied(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title":"valid title","markdown_content":"valid markdown body","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Subscriber"], body, "", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("subscriber create: got %d, want 403", rec.Code)
	}
}

func TestUpdatePostAdmin(t *testing.T) {
	env := setupPostWriteEnv(t)

	// Seed a post owned by Author.
	id, err := env.app.postsRepo.Create(t.Context(), postsrepo.PostInsert{
		AuthorID: env.userID["Author"], CategoryID: 1, Title: "old", Slug: "old-slug-1", Markdown: "old md", HTML: "old md",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := `{"title":"new","markdown_content":"new markdown body","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", id), env.tokens["Admin"], body, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin update: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Title != "new" || got.MarkdownContent != "new markdown body" {
		t.Errorf("response not updated: %+v", got)
	}

	persisted, _ := env.app.postsRepo.GetByID(t.Context(), id)
	if persisted.Title != "new" {
		t.Errorf("persisted title: got %q, want %q", persisted.Title, "new")
	}

	// author_id must not have changed.
	if persisted.AuthorID != env.userID["Author"] {
		t.Errorf("update changed author_id to %d", persisted.AuthorID)
	}
}

// TestUpdatePostNoOpStillReturns200 pins the pre-check behavior: a PUT that
// supplies values identical to what's already in the row should succeed.
// Without the GetPost pre-check the handler would see RowsAffected=0 from
// SQLite (it counts changed rows, not matched rows) and bogusly return 404.
func TestUpdatePostNoOpStillReturns200(t *testing.T) {
	env := setupPostWriteEnv(t)
	id, err := env.app.postsRepo.Create(t.Context(), postsrepo.PostInsert{
		AuthorID: env.userID["Author"], CategoryID: 1, Title: "same", Slug: "noop-slug", Markdown: "md", HTML: "rendered",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First request changes values — works either way.
	body := `{"title":"same title","markdown_content":"identical body","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", id), env.tokens["Admin"], body, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("first update: got %d, want 200", rec.Code)
	}

	// Second request with the exact same body — the row already matches, so
	// SQLite reports RowsAffected=0. The handler must still return 200.
	rec = doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", id), env.tokens["Admin"], body, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("no-op update: got %d, want 200 (regression in UpdatePost pre-check)", rec.Code)
	}
}

func TestUpdatePostAuthorOwnVsOther(t *testing.T) {
	env := setupPostWriteEnv(t)
	ctx := t.Context()

	// Create a second Author so we can test cross-author denial.
	otherID, err := env.app.usersRepo.Create(ctx, "other", "other@example.com", "h", 3)
	if err != nil {
		t.Fatalf("create other author: %v", err)
	}

	otherTok, err := env.app.issuer.Access(auth.UserClaim{UserID: otherID, Email: "other", Role: "Author", RoleID: 3})
	if err != nil {
		t.Fatalf("issue other token: %v", err)
	}

	// Seed a post owned by the original Author.
	postID, err := env.app.postsRepo.Create(ctx, postsrepo.PostInsert{
		AuthorID: env.userID["Author"], CategoryID: 1, Title: "t", Slug: "owner-test-slug", Markdown: "md", HTML: "md",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := `{"title":"changed","markdown_content":"changed markdown body","category_id":1}`

	// Author edits own post → 200.
	rec := doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", postID), env.tokens["Author"], body, "", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("author own: got %d, want 200", rec.Code)
	}

	// Other Author edits foreign post → bouncer 403.
	rec = doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", postID), otherTok, body, "", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("foreign author: got %d, want 403", rec.Code)
	}
}

func TestUpdatePostNotFoundAdmin(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title":"valid title","markdown_content":"valid body content","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPut, "/admin/posts/99999", env.tokens["Admin"], body, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("admin update missing: got %d, want 404", rec.Code)
	}
}

func TestDeletePostAdmin(t *testing.T) {
	env := setupPostWriteEnv(t)

	id, err := env.app.postsRepo.Create(t.Context(), postsrepo.PostInsert{
		AuthorID: env.userID["Author"], CategoryID: 1,
		Title: "doomed", Slug: "doomed-slug", Markdown: "md", HTML: "md",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := doJSON(t, env.app.r, http.MethodDelete, fmt.Sprintf("/admin/posts/%d", id), env.tokens["Admin"], "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	if _, err := env.app.postsRepo.GetByID(t.Context(), id); !errors.Is(err, postsrepo.ErrPostNotFound) {
		t.Errorf("after delete: got %v, want ErrPostNotFound", err)
	}
}

func TestDeletePostNotFoundAdmin(t *testing.T) {
	env := setupPostWriteEnv(t)
	rec := doJSON(t, env.app.r, http.MethodDelete, "/admin/posts/99999", env.tokens["Admin"], "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("delete missing: got %d, want 404", rec.Code)
	}
}

// TestCreatePostValidationEnvelope checks title, body, and category_id errors all surface in one accumulated 400 response.
func TestCreatePostValidationEnvelope(t *testing.T) {
	env := setupPostWriteEnv(t)
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Author"], `{"title":"hi","markdown_content":"too short"}`, "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}

	assertValidationFields(t, rec.Body.Bytes(), map[string]string{
		"title":            "must be at least 3 characters",
		"markdown_content": "must be at least 10 characters",
		"category_id":      "is required",
	})
}

// TestCreatePostSlugAutoSuffix pins the collision-resolution policy: two posts with the same title get distinct slugs (the second gets -2).
func TestCreatePostSlugAutoSuffix(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title":"Hello World","markdown_content":"the markdown body","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Author"], body, "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var first postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}

	if first.Slug != "hello-world" {
		t.Errorf("first slug: got %q, want hello-world", first.Slug)
	}

	rec = doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Author"], body, "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var second postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second: %v", err)
	}

	if second.Slug != "hello-world-2" {
		t.Errorf("second slug: got %q, want hello-world-2", second.Slug)
	}
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
