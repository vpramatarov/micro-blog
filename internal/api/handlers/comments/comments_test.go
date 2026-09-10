package comments_test

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
	categoriesrepo "github.com/vpramatarov/micro-blog/internal/api/repository/categories"
	commentsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/comments"
	postsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	rbacrepo "github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	settingsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/settings"
	shortlinksrepo "github.com/vpramatarov/micro-blog/internal/api/repository/shortlinks"
	tagsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/tags"
	tokensrepo "github.com/vpramatarov/micro-blog/internal/api/repository/tokens"
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

// appDeps wires the full router (posts service without the image pipeline - comment tests never upload) plus the repos tests seed fixtures through.
type appDeps struct {
	r            http.Handler
	usersRepo    *usersrepo.Repo
	postsRepo    *postsrepo.Repo
	commentsRepo *commentsrepo.Repo
	settingsRepo *settingsrepo.Repo
	issuer       *auth.Issuer
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
	tagsRepo := tagsrepo.New(db)
	settingsRepo := settingsrepo.New(db)
	commentsRepo := commentsrepo.New(db)
	cfg := &config.Config{JWTSecret: "test", JWTAccessTTL: 5 * time.Minute, JWTRefreshTTL: time.Hour}
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, auth.IssuerOptions{})
	authSvc := authh.New(cfg, usersRepo, tokensRepo, issuer, nil)
	usersSvc := usersh.New(cfg, usersRepo, rbacRepo, nil)
	postsSvc := postsh.New(postsRepo, categoriesRepo, tagsRepo, settingsRepo, nil, nil, nil, nil)
	shortlinksSvc := shortlinksh.New(shortLinksRepo, nil, nil)
	docsSvc := docsh.New(issuer, nil)
	categoriesSvc := categoriesh.New(categoriesRepo, nil)
	tagsSvc := tagsh.New(tagsRepo, nil)
	commentsSvc := commentsh.New(commentsRepo, postsRepo, settingsRepo, nil)
	settingsSvc := settingsh.New(settingsRepo, nil)
	r := router.New(
		router.Services{
			Auth: authSvc, Users: usersSvc, Posts: postsSvc,
			ShortLinks: shortlinksSvc, Docs: docsSvc,
			Categories: categoriesSvc, Tags: tagsSvc,
			Comments: commentsSvc, Settings: settingsSvc,
		},
		router.Middlewares{
			Auth:                 authmw.Authenticate(issuer, nil, nil),
			Bouncer:              rbacmw.Bouncer(rbacRepo, postsRepo, shortLinksRepo, nil),
			RequireAdmin:         rbacmw.RequireRole("Admin", nil),
			RequireEditorOrAdmin: rbacmw.RequireAnyRole(nil, "Admin", "Editor"),
		},
	)
	return &appDeps{
		r:            r,
		usersRepo:    usersRepo,
		postsRepo:    postsRepo,
		commentsRepo: commentsRepo,
		settingsRepo: settingsRepo,
		issuer:       issuer,
	}, db
}

func (app *appDeps) mustUser(t *testing.T, username string, roleID int64) int64 {
	t.Helper()
	id, err := app.usersRepo.Create(t.Context(), username, username+"@example.com", "h", roleID)
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}

	return id
}

func (app *appDeps) mustPost(t *testing.T, authorID int64, slug, status string) int64 {
	t.Helper()
	id, err := app.postsRepo.Create(t.Context(), postsrepo.PostInsert{
		AuthorID: authorID, CategoryID: 1,
		Title: "Fixture " + slug, Slug: slug,
		Markdown: "long enough markdown", HTML: "<p>long enough markdown</p>",
		Status: status,
	})
	if err != nil {
		t.Fatalf("create post %s: %v", slug, err)
	}

	return id
}

func (app *appDeps) issue(t *testing.T, uid int64, role string, roleID int64) string {
	t.Helper()
	token, err := app.issuer.Access(auth.UserClaim{UserID: uid, Email: "x", Role: role, RoleID: roleID})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	return token
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

type commentView struct {
	commentsrepo.Comment
	Replies []commentsrepo.Comment `json:"replies"`
}

type errorBody struct {
	Error   string            `json:"error"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields"`
}

func TestCreateCommentAllRoles(t *testing.T) {
	app, _ := buildApp(t)
	authorID := app.mustUser(t, "post-owner", 3)
	postID := app.mustPost(t, authorID, "roles-post", "published")
	cases := []struct {
		role   string
		roleID int64
	}{
		{"Admin", 1}, {"Editor", 2}, {"Author", 3}, {"Subscriber", 4},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			uid := app.mustUser(t, "commenter-"+strings.ToLower(tc.role), tc.roleID)
			token := app.issue(t, uid, tc.role, tc.roleID)
			rec := doJSON(t, app.r, http.MethodPost, fmt.Sprintf("/api/posts/%d/comments", postID), token,
				`{"content":"hello from `+tc.role+`"}`)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body.String())
			}

			var got commentView
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}

			if got.UserID != uid {
				t.Errorf("user_id: got %d, want %d", got.UserID, uid)
			}

			if got.AuthorName != "commenter-"+strings.ToLower(tc.role) {
				t.Errorf("author_name: got %q", got.AuthorName)
			}

			if got.Replies == nil || len(got.Replies) != 0 {
				t.Errorf("replies on fresh comment: got %v, want []", got.Replies)
			}
		})
	}
}

func TestCreateCommentOwnerFromClaimsNotBody(t *testing.T) {
	app, _ := buildApp(t)
	authorID := app.mustUser(t, "owner", 3)
	postID := app.mustPost(t, authorID, "claims-post", "published")
	uid := app.mustUser(t, "sub", 4)
	token := app.issue(t, uid, "Subscriber", 4)
	// A smuggled user_id must be ignored - ownership comes from the token.
	rec := doJSON(t, app.r, http.MethodPost, fmt.Sprintf("/api/posts/%d/comments", postID), token,
		fmt.Sprintf(`{"content":"smuggle test","user_id":%d}`, authorID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var got commentView
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.UserID != uid {
		t.Errorf("user_id: got %d, want the claims uid %d", got.UserID, uid)
	}
}

func TestCreateCommentRequiresAuth(t *testing.T) {
	app, _ := buildApp(t)
	rec := doJSON(t, app.r, http.MethodPost, "/api/posts/1/comments", "", `{"content":"anon"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: got %d, want 401", rec.Code)
	}
}

func TestCreateCommentPostVisibilityGates(t *testing.T) {
	app, _ := buildApp(t)
	authorID := app.mustUser(t, "owner", 3)
	draftID := app.mustPost(t, authorID, "draft-post", postsrepo.PostStatusDraft)
	archivedID := app.mustPost(t, authorID, "archived-post", postsrepo.PostStatusArchived)
	uid := app.mustUser(t, "sub", 4)
	token := app.issue(t, uid, "Subscriber", 4)

	for name, path := range map[string]string{
		"draft post":    fmt.Sprintf("/api/posts/%d/comments", draftID),
		"archived post": fmt.Sprintf("/api/posts/%d/comments", archivedID),
		"missing post":  "/api/posts/999999/comments",
	} {
		t.Run(name, func(t *testing.T) {
			rec := doJSON(t, app.r, http.MethodPost, path, token, `{"content":"hi there"}`)
			if rec.Code != http.StatusNotFound {
				t.Errorf("got %d, want 404 (non-published must equal missing)", rec.Code)
			}
		})
	}

	rec := doJSON(t, app.r, http.MethodPost, "/api/posts/abc/comments", token, `{"content":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-numeric id: got %d, want 400", rec.Code)
	}
}

func TestCreateCommentValidation(t *testing.T) {
	app, _ := buildApp(t)
	authorID := app.mustUser(t, "owner", 3)
	postID := app.mustPost(t, authorID, "validate-post", "published")
	uid := app.mustUser(t, "sub", 4)
	token := app.issue(t, uid, "Subscriber", 4)
	path := fmt.Sprintf("/api/posts/%d/comments", postID)
	rec := doJSON(t, app.r, http.MethodPost, path, token, `{"content":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank content: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	var eb errorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &eb)
	if eb.Fields["content"] != "is required" {
		t.Errorf("fields.content: got %q", eb.Fields["content"])
	}

	long := strings.Repeat("x", 2001)
	rec = doJSON(t, app.r, http.MethodPost, path, token, `{"content":"`+long+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("2001 chars: got %d, want 400", rec.Code)
	}

	_ = json.Unmarshal(rec.Body.Bytes(), &eb)
	if eb.Fields["content"] != "must be at most 2000 characters" {
		t.Errorf("fields.content: got %q", eb.Fields["content"])
	}

	// Content is trimmed before storage.
	rec = doJSON(t, app.r, http.MethodPost, path, token, `{"content":"  trimmed  "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("trim case: got %d, want 201", rec.Code)
	}

	var got commentView
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Content != "trimmed" {
		t.Errorf("content: got %q, want %q", got.Content, "trimmed")
	}
}

func TestCreateCommentToggleGates(t *testing.T) {
	app, _ := buildApp(t)
	ctx := t.Context()
	authorID := app.mustUser(t, "owner", 3)
	postID := app.mustPost(t, authorID, "toggle-post", "published")
	adminID := app.mustUser(t, "admin", 1)
	adminToken := app.issue(t, adminID, "Admin", 1)
	path := fmt.Sprintf("/api/posts/%d/comments", postID)
	// Seed one comment while commenting is open, so read-only mode has something to show.
	rec := doJSON(t, app.r, http.MethodPost, path, adminToken, `{"content":"pre-freeze"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed comment: got %d; body=%s", rec.Code, rec.Body.String())
	}

	var seeded commentView
	_ = json.Unmarshal(rec.Body.Bytes(), &seeded)
	// Per-post flag off -> 403 even for Admin.
	post, err := app.postsRepo.GetByID(ctx, postID)
	if err != nil {
		t.Fatalf("get post: %v", err)
	}

	disabled := false
	if err := app.postsRepo.Update(ctx, postID, postsrepo.PostUpdate{
		CategoryID: post.CategoryID, Title: post.Title, Markdown: post.MarkdownContent,
		HTML: post.HTMLContent, Slug: post.Slug, Status: post.Status,
		FeaturedImagePath: post.FeaturedImagePath, CommentsEnabled: disabled,
	}); err != nil {
		t.Fatalf("disable per-post flag: %v", err)
	}

	rec = doJSON(t, app.r, http.MethodPost, path, adminToken, `{"content":"blocked"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("per-post off: got %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	var eb errorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &eb)
	if eb.Error != "comments_disabled" {
		t.Errorf("error code: got %q, want comments_disabled", eb.Error)
	}

	// Existing comments stay readable, and moderation still works.
	list := doJSON(t, app.r, http.MethodGet, "/posts/toggle-post/comments", "", "")
	if list.Code != http.StatusOK {
		t.Fatalf("read-only list: got %d, want 200", list.Code)
	}

	if !strings.Contains(list.Body.String(), "pre-freeze") {
		t.Errorf("existing comment missing from read-only list: %s", list.Body.String())
	}

	del := doJSON(t, app.r, http.MethodDelete, fmt.Sprintf("/api/comments/%d", seeded.ID), adminToken, "")
	if del.Code != http.StatusNoContent {
		t.Errorf("moderation delete while disabled: got %d, want 204", del.Code)
	}

	// Re-enable per-post, then flip the GLOBAL switch off → 403 for everyone.
	enabled := true
	if err := app.postsRepo.Update(ctx, postID, postsrepo.PostUpdate{
		CategoryID: post.CategoryID, Title: post.Title, Markdown: post.MarkdownContent,
		HTML: post.HTMLContent, Slug: post.Slug, Status: post.Status,
		FeaturedImagePath: post.FeaturedImagePath, CommentsEnabled: enabled,
	}); err != nil {
		t.Fatalf("re-enable per-post flag: %v", err)
	}
	if err := app.settingsRepo.SetCommentsEnabled(ctx, false); err != nil {
		t.Fatalf("disable global switch: %v", err)
	}

	rec = doJSON(t, app.r, http.MethodPost, path, adminToken, `{"content":"blocked globally"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("global off: got %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	// Global back on → per-post flag (enabled) decides again.
	if err := app.settingsRepo.SetCommentsEnabled(ctx, true); err != nil {
		t.Fatalf("re-enable global switch: %v", err)
	}

	rec = doJSON(t, app.r, http.MethodPost, path, adminToken, `{"content":"open again"}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("after re-enable: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateReplyConstraints(t *testing.T) {
	app, _ := buildApp(t)
	ctx := t.Context()
	authorID := app.mustUser(t, "owner", 3)
	postID := app.mustPost(t, authorID, "reply-post", "published")
	otherPostID := app.mustPost(t, authorID, "other-post", "published")
	uid := app.mustUser(t, "sub", 4)
	token := app.issue(t, uid, "Subscriber", 4)
	path := fmt.Sprintf("/api/posts/%d/comments", postID)
	parentID, err := app.commentsRepo.Create(ctx, postID, uid, 0, "top-level")
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	otherParentID, err := app.commentsRepo.Create(ctx, otherPostID, uid, 0, "other post's comment")
	if err != nil {
		t.Fatalf("seed other parent: %v", err)
	}

	replyID, err := app.commentsRepo.Create(ctx, postID, uid, parentID, "a reply")
	if err != nil {
		t.Fatalf("seed reply: %v", err)
	}

	rec := doJSON(t, app.r, http.MethodPost, path, token,
		fmt.Sprintf(`{"content":"valid reply","parent_id":%d}`, parentID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid reply: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	var got commentView
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ParentID != parentID {
		t.Errorf("parent_id: got %d, want %d", got.ParentID, parentID)
	}

	cases := []struct {
		name     string
		parentID int64
		wantMsg  string
	}{
		{"missing parent", 999999, "does not exist"},
		{"parent on another post", otherParentID, "must belong to the same post"},
		{"parent is itself a reply", replyID, "must be a top-level comment"},
		{"negative parent", -1, "must be a positive id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(t, app.r, http.MethodPost, path, token,
				fmt.Sprintf(`{"content":"reply attempt","parent_id":%d}`, tc.parentID))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400; body=%s", rec.Code, rec.Body.String())
			}

			var eb errorBody
			_ = json.Unmarshal(rec.Body.Bytes(), &eb)
			if eb.Fields["parent_id"] != tc.wantMsg {
				t.Errorf("fields.parent_id: got %q, want %q", eb.Fields["parent_id"], tc.wantMsg)
			}
		})
	}
}

func TestListCommentsPublic(t *testing.T) {
	app, _ := buildApp(t)
	ctx := t.Context()
	authorID := app.mustUser(t, "owner", 3)
	postID := app.mustPost(t, authorID, "list-post", postsrepo.PostStatusPublished)
	app.mustPost(t, authorID, "list-draft", postsrepo.PostStatusDraft)
	uid := app.mustUser(t, "sub", 4)
	first, _ := app.commentsRepo.Create(ctx, postID, uid, 0, "first")
	second, _ := app.commentsRepo.Create(ctx, postID, uid, 0, "second")
	third, _ := app.commentsRepo.Create(ctx, postID, uid, 0, "third")
	replyA, _ := app.commentsRepo.Create(ctx, postID, uid, first, "reply to first")
	_ = replyA
	// Anonymous read.
	rec := doJSON(t, app.r, http.MethodGet, "/posts/list-post/comments", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var page struct {
		Items   []commentView `json:"items"`
		Page    int           `json:"page"`
		PerPage int           `json:"per_page"`
		Total   int           `json:"total"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if page.Total != 3 {
		t.Errorf("total: got %d, want 3 (top-level only, the reply must not count)", page.Total)
	}

	if len(page.Items) != 3 {
		t.Fatalf("items: got %d, want 3 top-level", len(page.Items))
	}

	if page.Items[0].ID != first || page.Items[1].ID != second || page.Items[2].ID != third {
		t.Errorf("order: got [%d %d %d], want oldest-first [%d %d %d]",
			page.Items[0].ID, page.Items[1].ID, page.Items[2].ID, first, second, third)
	}

	if len(page.Items[0].Replies) != 1 || page.Items[0].Replies[0].Content != "reply to first" {
		t.Errorf("first comment replies: got %+v", page.Items[0].Replies)
	}

	if len(page.Items[1].Replies) != 0 {
		t.Errorf("second comment replies: got %+v, want empty", page.Items[1].Replies)
	}

	// Reply-less comments must serialize "replies":[] (never null).
	if !strings.Contains(rec.Body.String(), `"replies":[]`) {
		t.Errorf(`body must contain "replies":[] for reply-less comments: %s`, rec.Body.String())
	}

	rec = doJSON(t, app.r, http.MethodGet, "/posts/list-post/comments?page=2&per_page=2", "", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0].ID != third || page.Total != 3 {
		t.Errorf("page 2: got %d items (total %d), want the single third comment", len(page.Items), page.Total)
	}

	rec = doJSON(t, app.r, http.MethodGet, "/posts/list-post/comments?per_page=zero", "", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad pagination: got %d, want 400", rec.Code)
	}

	rec = doJSON(t, app.r, http.MethodGet, "/posts/no-such-post/comments", "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug: got %d, want 404", rec.Code)
	}

	rec = doJSON(t, app.r, http.MethodGet, "/posts/list-draft/comments", "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("draft slug: got %d, want 404", rec.Code)
	}
}

// TestDeleteCommentAuthzMatrix pins the moderation rules: the parent post's
// author, Editor, and Admin may delete; the comment's own author may NOT.
func TestDeleteCommentAuthzMatrix(t *testing.T) {
	app, _ := buildApp(t)
	ctx := t.Context()
	postAuthorID := app.mustUser(t, "post-author", 3)
	otherAuthorID := app.mustUser(t, "other-author", 3)
	commenterID := app.mustUser(t, "commenter", 4)
	bystanderID := app.mustUser(t, "bystander", 4)
	editorID := app.mustUser(t, "editor", 2)
	adminID := app.mustUser(t, "admin", 1)
	postID := app.mustPost(t, postAuthorID, "authz-post", "published")
	app.mustPost(t, otherAuthorID, "authz-other-post", "published")

	newComment := func() int64 {
		t.Helper()
		id, err := app.commentsRepo.Create(ctx, postID, commenterID, 0, "moderate me")
		if err != nil {
			t.Fatalf("seed comment: %v", err)
		}
		return id
	}

	cases := []struct {
		who   string
		token string
		want  int
	}{
		{"comment owner (Subscriber)", app.issue(t, commenterID, "Subscriber", 4), http.StatusForbidden},
		{"unrelated Subscriber", app.issue(t, bystanderID, "Subscriber", 4), http.StatusForbidden},
		{"author of a different post", app.issue(t, otherAuthorID, "Author", 3), http.StatusForbidden},
		{"post author", app.issue(t, postAuthorID, "Author", 3), http.StatusNoContent},
		{"Editor", app.issue(t, editorID, "Editor", 2), http.StatusNoContent},
		{"Admin", app.issue(t, adminID, "Admin", 1), http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.who, func(t *testing.T) {
			id := newComment()
			rec := doJSON(t, app.r, http.MethodDelete, fmt.Sprintf("/api/comments/%d", id), tc.token, "")
			if rec.Code != tc.want {
				t.Errorf("got %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}

	adminToken := app.issue(t, adminID, "Admin", 1)
	if rec := doJSON(t, app.r, http.MethodDelete, "/api/comments/999999", adminToken, ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing id: got %d, want 404", rec.Code)
	}

	if rec := doJSON(t, app.r, http.MethodDelete, "/api/comments/abc", adminToken, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid id: got %d, want 400", rec.Code)
	}

	if rec := doJSON(t, app.r, http.MethodDelete, "/api/comments/1", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: got %d, want 401", rec.Code)
	}
}

func TestDeleteCommentCascadesRepliesInList(t *testing.T) {
	app, _ := buildApp(t)
	ctx := t.Context()
	authorID := app.mustUser(t, "owner", 3)
	postID := app.mustPost(t, authorID, "cascade-post", "published")
	uid := app.mustUser(t, "sub", 4)
	parentID, _ := app.commentsRepo.Create(ctx, postID, uid, 0, "parent")
	if _, err := app.commentsRepo.Create(ctx, postID, uid, parentID, "reply"); err != nil {
		t.Fatalf("seed reply: %v", err)
	}

	token := app.issue(t, authorID, "Author", 3)
	rec := doJSON(t, app.r, http.MethodDelete, fmt.Sprintf("/api/comments/%d", parentID), token, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	list := doJSON(t, app.r, http.MethodGet, "/posts/cascade-post/comments", "", "")
	var page struct {
		Items []commentView `json:"items"`
		Total int           `json:"total"`
	}
	_ = json.Unmarshal(list.Body.Bytes(), &page)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Errorf("after cascade: got %d items (total %d), want empty", len(page.Items), page.Total)
	}
}

// TestPostCarriesCommentMetadata pins the two count semantics and the computed comments_open field on the public post read.
func TestPostCarriesCommentMetadata(t *testing.T) {
	app, _ := buildApp(t)
	ctx := t.Context()
	authorID := app.mustUser(t, "owner", 3)
	postID := app.mustPost(t, authorID, "meta-post", "published")
	uid := app.mustUser(t, "sub", 4)
	parentID, _ := app.commentsRepo.Create(ctx, postID, uid, 0, "top-level")
	_, _ = app.commentsRepo.Create(ctx, postID, uid, parentID, "reply one")
	_, _ = app.commentsRepo.Create(ctx, postID, uid, parentID, "reply two")

	getPost := func() (view struct {
		postsrepo.Post
		CommentsOpen bool `json:"comments_open"`
	}) {
		t.Helper()
		rec := doJSON(t, app.r, http.MethodGet, "/posts/meta-post", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("get post: got %d; body=%s", rec.Code, rec.Body.String())
		}

		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode: %v", err)
		}

		return view
	}

	post := getPost()
	if post.CommentCount != 3 {
		t.Errorf("comment_count: got %d, want 3 (replies included)", post.CommentCount)
	}

	if !post.CommentsEnabled || !post.CommentsOpen {
		t.Errorf("toggles: enabled=%v open=%v, want both true", post.CommentsEnabled, post.CommentsOpen)
	}

	// The list total counts top-level only - deliberately not comment_count.
	list := doJSON(t, app.r, http.MethodGet, "/posts/meta-post/comments", "", "")
	var page struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(list.Body.Bytes(), &page)
	if page.Total != 1 {
		t.Errorf("list total: got %d, want 1 (top-level only)", page.Total)
	}

	// Global switch off → comments_open false while the raw flag stays true.
	if err := app.settingsRepo.SetCommentsEnabled(ctx, false); err != nil {
		t.Fatalf("disable global: %v", err)
	}

	post = getPost()
	if !post.CommentsEnabled || post.CommentsOpen {
		t.Errorf("global off: enabled=%v open=%v, want true/false", post.CommentsEnabled, post.CommentsOpen)
	}

	if post.CommentCount != 3 {
		t.Errorf("comment_count while disabled: got %d, want 3 (still counted)", post.CommentCount)
	}
}
