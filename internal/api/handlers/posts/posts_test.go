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
	"path/filepath"
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
	"github.com/vpramatarov/micro-blog/internal/markdown"
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

	q := fmt.Sprintf(`
		INSERT INTO posts (author_id, title, markdown_content, html_content, slug, status) VALUES (?, 'hello', '# hi', '<h1>hi</h1>', 'hello-public', '%s')`,
		postsrepo.PostStatusPublished,
	)
	res, err := raw.ExecContext(ctx, q, authorID)
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

	tagA, err := app.tagsRepo.Create(ctx, "go", "go")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}

	tagB, err := app.tagsRepo.Create(ctx, "web", "web")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}

	postID, err := app.postsRepo.Create(ctx, postsrepo.PostInsert{
		AuthorID: authorID, CategoryID: 1, Title: "Hello slug",
		Slug: "hello-slug", Markdown: "# hi", HTML: "<h1>hi</h1>",
		Status: postsrepo.PostStatusPublished,
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
		ID              int64            `json:"id"`
		Slug            string           `json:"slug"`
		AuthorID        int64            `json:"author_id"`
		AuthorName      string           `json:"author_name"`
		CategoryID      int64            `json:"category_id"`
		CategoryName    string           `json:"category_name"`
		MarkdownContent string           `json:"markdown_content"`
		Tags            map[int64]string `json:"tags"`
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

	excerpt := markdown.ToText(got.MarkdownContent)
	if excerpt != "hi" {
		t.Errorf("excerpt does not match: got %s, want hi", excerpt)
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

	excerpt := markdown.ToText(got.MarkdownContent)
	if excerpt != "hi\n\nworld" {
		t.Errorf("excerpt renderer not applied.")
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

func TestPostStatusVisibility(t *testing.T) {
	env := setupPostWriteEnv(t)
	ctx := t.Context()

	mustPost := func(slug, status string) {
		t.Helper()
		if _, err := env.app.postsRepo.Create(ctx, postsrepo.PostInsert{
			AuthorID: env.userID["Admin"], CategoryID: 1, Title: "vis-" + slug,
			Slug: slug, Markdown: "# body\n\nlong enough", HTML: "<p>x</p>",
			Status: status,
		}); err != nil {
			t.Fatalf("seed %s/%s: %v", status, slug, err)
		}
	}
	mustPost("live-1", postsrepo.PostStatusPublished)
	mustPost("wip-1", postsrepo.PostStatusDraft)
	mustPost("archive-1", postsrepo.PostStatusArchived)

	// public list - only "live-1" must be returned.
	rec := doJSON(t, env.app.r, http.MethodGet, "/posts", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("public list: got %d", rec.Code)
	}

	var pubList struct {
		Items []postsrepo.Post `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pubList); err != nil {
		t.Fatalf("decode public list: %v", err)
	}

	if pubList.Total != 1 || len(pubList.Items) != 1 {
		t.Errorf("public list got %d/%d items, want 1/1", len(pubList.Items), pubList.Total)
	}

	if len(pubList.Items) == 1 && pubList.Items[0].Slug != "live-1" {
		t.Errorf("public list returned non-published slug: %q", pubList.Items[0].Slug)
	}

	// public single read - draft slug 404s
	rec = doJSON(t, env.app.r, http.MethodGet, "/posts/wip-1", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("public draft slug: got %d, want 404", rec.Code)
	}

	// archived behaves the same.
	rec = doJSON(t, env.app.r, http.MethodGet, "/posts/archive-1", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("public archived slug: got %d, want 404", rec.Code)
	}

	// published is reachable
	rec = doJSON(t, env.app.r, http.MethodGet, "/posts/live-1", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("public published slug: got %d, want 200", rec.Code)
	}

	// Admin list with no filter - all 3.
	rec = doJSON(t, env.app.r, http.MethodGet, "/admin/posts", env.tokens["Admin"], "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list: got %d, want 200", rec.Code)
	}

	var adminList struct {
		Items []postsrepo.Post `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &adminList); err != nil {
		t.Fatalf("decode admin list: %v", err)
	}

	if adminList.Total != 3 {
		t.Errorf("admin list (no filter): got total=%d, want 3", adminList.Total)
	}

	rec = doJSON(t, env.app.r, http.MethodGet, fmt.Sprintf("/admin/posts?status=%s", postsrepo.PostStatusDraft), env.tokens["Admin"], "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin filter (filter by status %s): got %d, want 200", postsrepo.PostStatusDraft, rec.Code)
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &adminList); err != nil {
		t.Fatalf("decode admin list (filter by status %s): %v", postsrepo.PostStatusDraft, err)
	}

	if adminList.Total != 1 || (len(adminList.Items) == 1 && adminList.Items[0].Slug != "wip-1") {
		t.Errorf("admin ?status=%s: got %d items (first=%q), want 1 (wip-1)", postsrepo.PostStatusDraft, adminList.Total, adminList.Items[0].Slug)
	}

	// Admin list with status that do not exists
	rec = doJSON(t, env.app.r, http.MethodGet, "/admin/posts?status=bogus", env.tokens["Admin"], "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("admin ?status=bogus: got %d, want 400", rec.Code)
	}
}

func TestCreatePostDefaultsToDraft(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title": "new draft", "markdown_content": "markdown body", "category_id": 1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], body, "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d body=%s, want 201", rec.Code, rec.Body.String())
	}

	var created postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if created.Status != postsrepo.PostStatusDraft {
		t.Errorf("default status: got %q, want %s", created.Status, postsrepo.PostStatusDraft)
	}

	// public list must not contain it.
	rec = doJSON(t, env.app.r, http.MethodGet, "/posts", "", "")
	var list struct {
		Items []postsrepo.Post `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode public: %v", err)
	}

	for _, p := range list.Items {
		if p.ID == created.ID {
			t.Errorf("new %s leaked onto public /posts list", postsrepo.PostStatusDraft)
		}
	}
}

func TestCreatePostAcceptsExplicitStatus(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := fmt.Sprintf(`{"title": "go live", "markdown_content": "markdown body", "category_id": 1, "status": "%s"}`, postsrepo.PostStatusPublished)
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], body, "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d body=%s, want 201", rec.Code, rec.Body.String())
	}

	var created postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if created.Status != postsrepo.PostStatusPublished {
		t.Errorf("status: got %q, want %s", created.Status, postsrepo.PostStatusPublished)
	}

	// public single read works
	rec = doJSON(t, env.app.r, http.MethodGet, "/posts/"+created.Slug, "", "")
	if rec.Code != http.StatusOK {
		t.Errorf("public read of freshly published: got %d", rec.Code)
	}
}

func TestCreatePostRejectsNonExistingStatus(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title": "bogus", "markdown_content": "markdown body", "category_id": 1, "status": "sneaky"}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], body, "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d body=%s, want 400", rec.Code, rec.Body.String())
	}

	msg := fmt.Sprintf("must be one of: %s, %s, %s", postsrepo.PostStatusDraft, postsrepo.PostStatusPublished, postsrepo.PostStatusArchived)
	assertValidationFields(t, rec.Body.Bytes(), map[string]string{"status": msg})
}

func TestUpdateStatusChange(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title": "go live", "markdown_content": "markdown body", "category_id": 1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], body, "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d body=%s, want 201", rec.Code, rec.Body.String())
	}

	var created postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if created.Status != postsrepo.PostStatusDraft {
		t.Errorf("expected %s, got %q", postsrepo.PostStatusDraft, created.Status)
	}

	publishBody := fmt.Sprintf(`{"title": "go live", "markdown_content": "markdown body", "category_id": 1, "status": "%s"}`, postsrepo.PostStatusPublished)
	updateEndpoint := fmt.Sprintf("/admin/posts/%d", created.ID)
	rec = doMultipartPost(t, env.app.r, http.MethodPut, updateEndpoint, env.tokens["Admin"], publishBody, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish PUT: got %d body=%s, want 200", rec.Code, rec.Body.String())
	}

	var published postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &published); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if published.Status != postsrepo.PostStatusPublished {
		t.Errorf("after publish: got %q, want %s", published.Status, postsrepo.PostStatusPublished)
	}

	keepBody := `{"title": "go live", "markdown_content": "markdown body", "category_id": 1}`
	rec = doMultipartPost(t, env.app.r, http.MethodPut, updateEndpoint, env.tokens["Admin"], keepBody, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("keep PUT: got %d body=%s, want 200", rec.Code, rec.Body.String())
	}

	var kept postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &kept); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if kept.Status != postsrepo.PostStatusPublished {
		t.Errorf("omitted status should preserve existing: got %q, want %s", kept.Status, postsrepo.PostStatusPublished)
	}
}

// categoryView decodes the shape returned by the category write endpoints.
type categoryView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// TestCreateCategoryAutoSlug verifies the auto-generate path: the handler runs slug.Generate(name) when the client omits `slug`.
func TestCreateCategoryAutoSlug(t *testing.T) {
	app, _ := buildApp(t)
	tok := editorToken(t, app)
	rec := doJSON(t, app.r, http.MethodPost, "/admin/categories", tok, `{"name":"Backend Engineering"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var got categoryView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Slug != "backend-engineering" {
		t.Errorf("auto slug: got %q, want backend-engineering", got.Slug)
	}
}

// TestCreateCategoryExplicitSlug verifies a supplied slug is persisted as-is (not regenerated from the name).
func TestCreateCategoryExplicitSlug(t *testing.T) {
	app, _ := buildApp(t)
	tok := editorToken(t, app)
	rec := doJSON(t, app.r, http.MethodPost, "/admin/categories", tok, `{"name":"Engineering","slug":"eng"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var got categoryView
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Slug != "eng" {
		t.Errorf("explicit slug: got %q, want eng", got.Slug)
	}
}

// TestCreateCategoryExplicitSlugConflict pins the explicit-collision policy:
// a client-supplied slug that already exists is rejected with 409 slug_conflict instead of silently auto-suffixed.
func TestCreateCategoryExplicitSlugConflict(t *testing.T) {
	app, _ := buildApp(t)
	tok := editorToken(t, app)
	rec := doJSON(t, app.r, http.MethodPost, "/admin/categories", tok, `{"name":"Engineering","slug":"eng"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, app.r, http.MethodPost, "/admin/categories", tok,
		`{"name":"Engineering Reloaded","slug":"eng"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict: got %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	var env struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error != "slug_conflict" {
		t.Errorf("error code: got %q, want slug_conflict", env.Error)
	}
}

// TestCreateCategoryBogusSlug verifies the slug validator rejects non kebab-case ASCII with the 400 validation envelope.
func TestCreateCategoryBogusSlug(t *testing.T) {
	app, _ := buildApp(t)
	tok := editorToken(t, app)
	rec := doJSON(t, app.r, http.MethodPost, "/admin/categories", tok, `{"name":"Engineering","slug":"Foo Bar"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus slug: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestUpdateCategoryPreservesSlugWhenOmitted verifies the PUT-omits-slug
func TestUpdateCategoryPreservesSlugWhenOmitted(t *testing.T) {
	app, _ := buildApp(t)
	tok := editorToken(t, app)
	rec := doJSON(t, app.r, http.MethodPost, "/admin/categories", tok, `{"name":"Engineering","slug":"eng"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created categoryView
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	updatePath := fmt.Sprintf("/admin/categories/%d", created.ID)
	rec = doJSON(t, app.r, http.MethodPut, updatePath, tok, `{"name":"Backend Engineering"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var updated categoryView
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Slug != "eng" {
		t.Errorf("slug after rename: got %q, want eng (preserved)", updated.Slug)
	}

	if updated.Name != "Backend Engineering" {
		t.Errorf("name: got %q, want Backend Engineering", updated.Name)
	}
}

// postsByCategoryResp / postsByTagResp mirror the wire shapes of PostsByCategoryResponse / PostsByTagResponse.
// Inlined here so the tests decode against the JSON contract, not the Go struct (catches accidental schema drift).
type postsByCategoryResp struct {
	Category struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"category"`
	Items   []postsrepo.Post `json:"items"`
	Page    int              `json:"page"`
	PerPage int              `json:"per_page"`
	Total   int              `json:"total"`
}

type postsByTagResp struct {
	Tag struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"tag"`
	Items   []postsrepo.Post `json:"items"`
	Page    int              `json:"page"`
	PerPage int              `json:"per_page"`
	Total   int              `json:"total"`
}

// TestListPostsByCategorySlugPublic verifies the public endpoint only returns
// `status='published'` posts and 404s on an unknown slug.
func TestListPostsByCategorySlugPublic(t *testing.T) {
	app, raw := buildApp(t)
	ctx := t.Context()
	authorID, err := app.usersRepo.Create(ctx, "writer", "writer@example.com", "h", 3)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	catID, err := app.categoriesRepo.Create(ctx, "Engineering", "engineering")
	if err != nil {
		t.Fatalf("create category: %v", err)
	}

	mustPost := func(title, slug, status string) {
		t.Helper()
		if _, err := raw.ExecContext(ctx,
			`INSERT INTO posts (author_id, category_id, title, markdown_content, html_content, slug, status) VALUES (?, ?, ?, '', '', ?, ?)`,
			authorID, catID, title, slug, status,
		); err != nil {
			t.Fatalf("insert post: %v", err)
		}
	}
	mustPost("p-published", "p-published", "published")
	mustPost("p-draft", "p-draft", "draft")
	mustPost("p-archived", "p-archived", "archived")
	rec := doJSON(t, app.r, http.MethodGet, "/categories/engineering", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got postsByCategoryResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}

	if got.Category.ID != catID || got.Category.Slug != "engineering" {
		t.Errorf("category metadata: got %+v", got.Category)
	}

	if got.Total != 1 || len(got.Items) != 1 || got.Items[0].Slug != "p-published" {
		t.Errorf("items: total=%d items=%d, want 1 published only; body=%s", got.Total, len(got.Items), rec.Body.String())
	}

	// Unknown slug → 404.
	rec = doJSON(t, app.r, http.MethodGet, "/categories/no-such-slug", "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug: got %d, want 404", rec.Code)
	}
}

// TestListPostsByTagSlugPublic verifies the tag pivot returns only published posts attached to the tag and 404s on miss.
func TestListPostsByTagSlugPublic(t *testing.T) {
	app, raw := buildApp(t)
	ctx := t.Context()
	authorID, err := app.usersRepo.Create(ctx, "writer", "writer@example.com", "h", 3)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	tagID, err := app.tagsRepo.Create(ctx, "Go", "go")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	// Different tag — its posts must not leak into the /tags/go response.
	otherTagID, _ := app.tagsRepo.Create(ctx, "Web", "web")
	mustPostWithTags := func(title, slug, status string, tagIDs []int64) int64 {
		t.Helper()
		res, err := raw.ExecContext(ctx,
			`INSERT INTO posts (author_id, category_id, title, markdown_content, html_content, slug, status) VALUES (?, 1, ?, '', '', ?, ?)`,
			authorID, title, slug, status,
		)
		if err != nil {
			t.Fatalf("insert post: %v", err)
		}

		id, _ := res.LastInsertId()
		if err := app.tagsRepo.ReplaceForPost(ctx, id, tagIDs); err != nil {
			t.Fatalf("attach tags: %v", err)
		}

		return id
	}
	mustPostWithTags("tagged-published", "tagged-published", "published", []int64{tagID})
	mustPostWithTags("tagged-draft", "tagged-draft", "draft", []int64{tagID})
	mustPostWithTags("other-tag", "other-tag", "published", []int64{otherTagID})
	mustPostWithTags("untagged", "untagged", "published", nil)
	rec := doJSON(t, app.r, http.MethodGet, "/tags/go", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got postsByTagResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Tag.ID != tagID || got.Tag.Slug != "go" {
		t.Errorf("tag metadata: got %+v", got.Tag)
	}

	if got.Total != 1 || len(got.Items) != 1 || got.Items[0].Slug != "tagged-published" {
		t.Errorf("items: total=%d items=%d, want 1 published-tagged only; body=%s", got.Total, len(got.Items), rec.Body.String())
	}

	rec = doJSON(t, app.r, http.MethodGet, "/tags/no-such-slug", "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug: got %d, want 404", rec.Code)
	}
}

// TestListPostsByCategorySlugAdminRoleFilter pins the Author-sees-only-own /
// other-roles-see-all split for the admin pivot endpoint, mirroring the /admin/posts matrix.
func TestListPostsByCategorySlugAdminRoleFilter(t *testing.T) {
	app, raw := buildApp(t)
	ctx := t.Context()
	mustUser := func(username, email string, roleID int64) int64 {
		t.Helper()
		id, err := app.usersRepo.Create(ctx, username, email, "h", roleID)
		if err != nil {
			t.Fatalf("create user: %v", err)
		}
		return id
	}
	aliceID := mustUser("alice", "alice@example.com", 3) // Author
	bobID := mustUser("bob", "bob@example.com", 3)       // Author
	edID := mustUser("ed", "ed@example.com", 2)          // Editor
	catID, _ := app.categoriesRepo.Create(ctx, "Engineering", "engineering")
	mustPost := func(authorID int64, slug, status string) {
		t.Helper()
		if _, err := raw.ExecContext(ctx,
			`INSERT INTO posts (author_id, category_id, title, markdown_content, html_content, slug, status) VALUES (?, ?, ?, '', '', ?, ?)`,
			authorID, catID, slug, slug, status,
		); err != nil {
			t.Fatalf("insert post: %v", err)
		}
	}
	mustPost(aliceID, "alice-draft", "draft")
	mustPost(aliceID, "alice-published", "published")
	mustPost(bobID, "bob-draft", "draft")
	// 3 posts total in this category. Author scope sees own (alice=2, bob=1).
	issue := func(uid int64, role string, roleID int64) string {
		tok, err := app.issuer.Access(auth.UserClaim{UserID: uid, Email: "x", Role: role, RoleID: roleID})
		if err != nil {
			t.Fatalf("issue token: %v", err)
		}
		return tok
	}

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"alice (Author)", issue(aliceID, "Author", 3), 2},
		{"bob (Author)", issue(bobID, "Author", 3), 1},
		{"ed (Editor)", issue(edID, "Editor", 2), 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doJSON(t, app.r, http.MethodGet, "/admin/categories/engineering", tt.token, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
			}

			var got postsByCategoryResp
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}

			if got.Total != tt.want || len(got.Items) != tt.want {
				t.Errorf("got total=%d items=%d, want %d", got.Total, len(got.Items), tt.want)
			}
		})
	}
}

// TestListPostsByCategorySlugAdminStatusFilter exercises the ?status= query parameter —
// same semantics as /admin/posts (empty = all statuses, enum value = filter, bogus value = 400).
func TestListPostsByCategorySlugAdminStatusFilter(t *testing.T) {
	app, raw := buildApp(t)
	ctx := t.Context()
	edID, err := app.usersRepo.Create(ctx, "ed", "ed@example.com", "h", 2)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	catID, _ := app.categoriesRepo.Create(ctx, "Eng", "eng")
	for _, st := range []string{"draft", "published", "archived"} {
		if _, err := raw.ExecContext(ctx,
			`INSERT INTO posts (author_id, category_id, title, markdown_content, html_content, slug, status) VALUES (?, ?, ?, '', '', ?, ?)`,
			edID, catID, st, "post-"+st, st,
		); err != nil {
			t.Fatalf("insert %s: %v", st, err)
		}
	}

	tok, err := app.issuer.Access(auth.UserClaim{UserID: edID, Email: "x", Role: "Editor", RoleID: 2})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// No filter → all 3.
	rec := doJSON(t, app.r, http.MethodGet, "/admin/categories/eng", tok, "")
	var got postsByCategoryResp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Total != 3 {
		t.Errorf("no filter: total=%d, want 3", got.Total)
	}

	// status=draft → 1.
	rec = doJSON(t, app.r, http.MethodGet, "/admin/categories/eng?status=draft", tok, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Total != 1 || got.Items[0].Status != "draft" {
		t.Errorf("status=draft: total=%d items[0].status=%q, want 1/draft", got.Total, got.Items[0].Status)
	}

	// status=bogus → 400 validation envelope.
	rec = doJSON(t, app.r, http.MethodGet, "/admin/categories/eng?status=bogus", tok, "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=bogus: got %d, want 400", rec.Code)
	}

	// Anonymous → 401 on admin route.
	rec = doJSON(t, app.r, http.MethodGet, "/admin/categories/eng", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous on admin: got %d, want 401", rec.Code)
	}

	// Unknown slug → 404 (even for an authed Editor).
	rec = doJSON(t, app.r, http.MethodGet, "/admin/categories/no-such", tok, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug admin: got %d, want 404", rec.Code)
	}
}

// TestListPostsByTagSlugAdminMatrix runs the same role + status matrix against the tag pivot.
// Lighter than the category test because the join shape is the only difference — the role/status logic is shared.
func TestListPostsByTagSlugAdminMatrix(t *testing.T) {
	app, raw := buildApp(t)
	ctx := t.Context()
	aliceID, _ := app.usersRepo.Create(ctx, "alice", "alice@example.com", "h", 3)
	bobID, _ := app.usersRepo.Create(ctx, "bob", "bob@example.com", "h", 3)
	edID, _ := app.usersRepo.Create(ctx, "ed", "ed@example.com", "h", 2)
	tagID, _ := app.tagsRepo.Create(ctx, "Go", "go")
	mustPost := func(authorID int64, slug, status string) int64 {
		t.Helper()
		res, err := raw.ExecContext(ctx,
			`INSERT INTO posts (author_id, category_id, title, markdown_content, html_content, slug, status) VALUES (?, 1, ?, '', '', ?, ?)`,
			authorID, slug, slug, status,
		)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}

		id, _ := res.LastInsertId()
		if err := app.tagsRepo.ReplaceForPost(ctx, id, []int64{tagID}); err != nil {
			t.Fatalf("attach tag: %v", err)
		}

		return id
	}
	mustPost(aliceID, "alice-draft", "draft")
	mustPost(aliceID, "alice-published", "published")
	mustPost(bobID, "bob-draft", "draft")
	issue := func(uid int64, role string, roleID int64) string {
		tok, err := app.issuer.Access(auth.UserClaim{UserID: uid, Email: "x", Role: role, RoleID: roleID})
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		return tok
	}

	// Editor sees all three regardless of status.
	rec := doJSON(t, app.r, http.MethodGet, "/admin/tags/go", issue(edID, "Editor", 2), "")
	var got postsByTagResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}

	if got.Tag.Slug != "go" || got.Total != 3 {
		t.Errorf("editor: got tag=%q total=%d, want go/3", got.Tag.Slug, got.Total)
	}

	// Alice (Author) sees own 2.
	rec = doJSON(t, app.r, http.MethodGet, "/admin/tags/go", issue(aliceID, "Author", 3), "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Total != 2 {
		t.Errorf("alice author own: got %d, want 2", got.Total)
	}

	// Alice + status=draft sees own 1.
	rec = doJSON(t, app.r, http.MethodGet, "/admin/tags/go?status=draft", issue(aliceID, "Author", 3), "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Total != 1 || got.Items[0].Slug != "alice-draft" {
		t.Errorf("alice draft: total=%d slug=%q, want 1/alice-draft", got.Total, got.Items[0].Slug)
	}
}

func TestUpdatePostPartialTitleOnly(t *testing.T) {
	env := setupPostWriteEnv(t)
	id, err := env.app.postsRepo.Create(t.Context(), postsrepo.PostInsert{
		AuthorID: env.userID["Author"], CategoryID: 1, Title: "original title",
		Slug: "original-title", Markdown: "the **original** markdown content.",
		HTML: "the <strong>original</strong> markdown content.", Status: "published",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", id), env.tokens["Admin"], `{"title": "only the title changed."}`, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("partial title PUT: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Title != "only the title changed." {
		t.Errorf("title: got %q, want %q", got.Title, "only the title changed.")
	}

	if got.MarkdownContent != "the **original** markdown content." {
		t.Errorf("Markdown not preserved: got %q", got.MarkdownContent)
	}

	if got.HTMLContent != "the <strong>original</strong> markdown content." {
		t.Errorf("HTML not preserved: got %q", got.HTMLContent)
	}

	if got.CategoryID != 1 {
		t.Errorf("Category not preserved: got %d, want 1", got.CategoryID)
	}

	if got.Status != "published" {
		t.Errorf("Status not preserved: got %q, want published", got.Status)
	}
}

func TestUpdatePostPartialMarkdownOnly(t *testing.T) {
	env := setupPostWriteEnv(t)
	id, err := env.app.postsRepo.Create(t.Context(), postsrepo.PostInsert{
		AuthorID: env.userID["Author"], CategoryID: 1, Title: "original title",
		Slug: "original-title", Markdown: "the **original** markdown content.",
		HTML: "the <strong>original</strong> markdown content.", Status: "published",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", id), env.tokens["Admin"], `{"markdown_content": "# Fresh\n\nbrand new body."}`, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("partial markdown PUT: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Title != "original title" {
		t.Errorf("title: got %q, want original title", got.Title)
	}

	if got.MarkdownContent != "# Fresh\n\nbrand new body." {
		t.Errorf("Markdown not preserved: got %q", got.MarkdownContent)
	}

	if !strings.Contains(got.HTMLContent, "<h1>Fresh</h1>") {
		t.Errorf("HTML not re-rendered: got %q", got.HTMLContent)
	}

	if got.CategoryID != 1 {
		t.Errorf("Category not preserved: got %d, want 1", got.CategoryID)
	}

	if got.Status != "published" {
		t.Errorf("Status not preserved: got %q, want published", got.Status)
	}
}

func TestUpdatePostPartialCategoryOnly(t *testing.T) {
	env := setupPostWriteEnv(t)
	ctx := t.Context()
	catID, err := env.app.categoriesRepo.Create(ctx, "Engineering", "engineering")
	if err != nil {
		t.Fatalf("create category: %v", err)
	}

	id, err := env.app.postsRepo.Create(t.Context(), postsrepo.PostInsert{
		AuthorID: env.userID["Author"], CategoryID: 1, Title: "original title",
		Slug: "original-title", Markdown: "the **original** markdown content.",
		HTML: "the <strong>original</strong> markdown content.", Status: "published",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", id), env.tokens["Admin"], fmt.Sprintf(`{"category_id": %d}`, catID), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("partial category PUT: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Title != "original title" {
		t.Errorf("title: got %q, want original title", got.Title)
	}

	if got.MarkdownContent != "the **original** markdown content." {
		t.Errorf("Markdown not preserved: got %q", got.MarkdownContent)
	}

	if got.CategoryID != catID {
		t.Errorf("category_id: got %d, want %d", got.CategoryID, catID)
	}

	if got.CategoryName != "Engineering" {
		t.Errorf("category_name: got %q, want Engineering", got.CategoryName)
	}

	if got.Status != "published" {
		t.Errorf("Status not preserved: got %q, want published", got.Status)
	}
}

func TestUpdatePostPresentEmptyTitleRejected(t *testing.T) {
	env := setupPostWriteEnv(t)
	id, err := env.app.postsRepo.Create(t.Context(), postsrepo.PostInsert{
		AuthorID: env.userID["Author"], CategoryID: 1, Title: "original title",
		Slug: "original-title", Markdown: "the **original** markdown content.",
		HTML: "the <strong>original</strong> markdown content.", Status: "published",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", id), env.tokens["Admin"], `{"title": ""}`, "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty title PUT: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	assertValidationFields(t, rec.Body.Bytes(), map[string]string{"title": "is required"})
}

func TestUpdatePostEmptyBodyPreservesAll(t *testing.T) {
	env := setupPostWriteEnv(t)
	ctx := t.Context()
	id, err := env.app.postsRepo.Create(ctx, postsrepo.PostInsert{
		AuthorID: env.userID["Author"], CategoryID: 1, Title: "original title",
		Slug: "original-title", Markdown: "the **original** markdown content.",
		HTML: "the <strong>original</strong> markdown content.", Status: "published",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", id), env.tokens["Admin"], `{}`, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty body PUT: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got postsrepo.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Title != "original title" {
		t.Errorf("title: got %q, want original title", got.Title)
	}

	if got.MarkdownContent != "the **original** markdown content." {
		t.Errorf("Markdown not preserved: got %q", got.MarkdownContent)
	}

	if got.HTMLContent != "the <strong>original</strong> markdown content." {
		t.Errorf("Markdown not preserved: got %q", got.MarkdownContent)
	}

	if got.CategoryID != 1 {
		t.Errorf("category_id: got %d, want 1", got.CategoryID)
	}

	if got.Status != "published" {
		t.Errorf("Status not preserved: got %q, want published", got.Status)
	}
}

func TestUpdatePostImageOnlyAddsImage(t *testing.T) {
	env := setupPostWriteEnv(t)
	createBody := `{"title": "needs an image", "markdown_content": "# body content here", "category_id": 1, "status": "published"}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], createBody, "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("empty body POST: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID                int64  `json:"id"`
		Title             string `json:"title"`
		MarkdownContent   string `json:"markdown_content"`
		CategoryID        int64  `json:"category_id"`
		Status            string `json:"status"`
		FeaturedImagePath string `json:"featured_image_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	if created.FeaturedImagePath != "" {
		t.Fatalf("precondition: post should start with no image, got %q", created.FeaturedImagePath)
	}

	rec = doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", created.ID), env.tokens["Admin"], "", "added.png", makePNG(t, 1024, 1024))
	if rec.Code != http.StatusOK {
		t.Fatalf("image only update PUT: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var updated struct {
		Title             string `json:"title"`
		MarkdownContent   string `json:"markdown_content"`
		CategoryID        int64  `json:"category_id"`
		Status            string `json:"status"`
		FeaturedImagePath string `json:"featured_image_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}

	if updated.FeaturedImagePath == "" || !strings.HasSuffix(updated.FeaturedImagePath, ".png") {
		t.Fatalf("featured image path: got %q, want .png path", created.FeaturedImagePath)
	}

	if updated.Title != created.Title || updated.MarkdownContent != created.MarkdownContent ||
		updated.CategoryID != created.CategoryID || updated.Status != created.Status {
		t.Errorf("image only update changed a non-image field:\n got %+v\n want %+v", updated, created)
	}

	originallFull := filepath.Join(env.app.uploadsRoot, filepath.FromSlash(updated.FeaturedImagePath))
	if _, err := os.Stat(originallFull); err != nil {
		t.Errorf("original missing on disk: %v", err)
	}
}

func TestUpdatePostImageOnlyPreservesTags(t *testing.T) {
	env := setupPostWriteEnv(t)
	ctx := t.Context()
	tag1, err := env.app.tagsRepo.Create(ctx, "go", "go")
	if err != nil {
		t.Fatalf("create tag go: %v", err)
	}

	tag2, err := env.app.tagsRepo.Create(ctx, "web", "web")
	if err != nil {
		t.Fatalf("create tag web: %v", err)
	}

	createBody := fmt.Sprintf(
		`{"title": "tagged post", "markdown_content": "# body content here", "category_id": 1, "tag_ids": [%d, %d]}`,
		tag1, tag2,
	)
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], createBody, "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID   int64            `json:"id"`
		Tags map[int64]string `json:"tags"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	if len(created.Tags) != 2 {
		t.Fatalf("precondition: expected 2 tags on create, got %v", created.Tags)
	}

	// PUT image only - both tags must survive.
	rec = doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", created.ID), env.tokens["Admin"], "", "pic.jpg", makeJPEG(t, 1024, 1024))
	if rec.Code != http.StatusOK {
		t.Fatalf("update image only: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var updated struct {
		Tags map[int64]string `json:"tags"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}

	if len(updated.Tags) != 2 || updated.Tags[tag1] != "go" || updated.Tags[tag2] != "web" {
		t.Errorf("image only chaged tags: got %v, want {%d:go, %d,web}", updated.Tags, tag1, tag2)
	}
}

func TestUpdatePostNoDataNoImageBadRequest(t *testing.T) {
	env := setupPostWriteEnv(t)
	createBody := `{"title": "post heading", "markdown_content": "# body content here", "category_id": 1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], createBody, "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	rec = doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", created.ID), env.tokens["Admin"], "", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("neither data nor image: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreatePostImageOnlyRejected(t *testing.T) {
	env := setupPostWriteEnv(t)
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], "", "x.png", makePNG(t, 1024, 1024))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create without data: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	assertValidationFields(t, rec.Body.Bytes(), map[string]string{"data": "is required (JSON-encoded post fields)"})
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

// editorToken issues an Editor JWT through buildApp's issuer for category / tag write tests.
// The Editor role is enough to satisfy RequireEditorOrAdmin on POST /admin/categories.
func editorToken(t *testing.T, app *appDeps) string {
	t.Helper()
	ctx := t.Context()
	id, err := app.usersRepo.Create(ctx, "ed", "ed@example.com", "h", 2)
	if err != nil {
		t.Fatalf("seed editor: %v", err)
	}

	tok, err := app.issuer.Access(auth.UserClaim{UserID: id, Email: "ed@example.com", Role: "Editor", RoleID: 2})
	if err != nil {
		t.Fatalf("issue editor token: %v", err)
	}

	return tok
}
