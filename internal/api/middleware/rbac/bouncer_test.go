package rbac_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	authmw "github.com/vpramatarov/micro-blog/internal/api/middleware/auth"
	rbacmw "github.com/vpramatarov/micro-blog/internal/api/middleware/rbac"
	postsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	rbacrepo "github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	shortlinksrepo "github.com/vpramatarov/micro-blog/internal/api/repository/shortlinks"
	usersrepo "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// setupBouncerEnv stands up an in-memory chi router mirroring the production
// /admin layout (Authenticate at /admin, Bouncer on the post-write subgroup,
// plain auth on /admin/post/{id}) with a DB pre-populated with one user per role and an Alice-owned post.
type bouncerEnv struct {
	srv         *httptest.Server
	issuer      *auth.Issuer
	authorAlice ownedUser
	authorBob   ownedUser
	editorEd    ownedUser
	adminCarol  ownedUser
	subEve      ownedUser
	alicePostID int64
}

type ownedUser struct {
	ID     int64
	Email  string
	Role   string
	RoleID int64
}

func setupBouncerEnv(t *testing.T) bouncerEnv {
	t.Helper()
	db := testutil.SetupTestDB(t)
	urepo := usersrepo.New(db)
	prepo := postsrepo.New(db)
	srepo := shortlinksrepo.New(db)
	rrepo := rbacrepo.New(db)
	ctx := t.Context()

	issuer := auth.NewIssuer("test-secret", 5*time.Minute, auth.IssuerOptions{})

	mustUser := func(username, email string, roleID int64, roleName string) ownedUser {
		id, err := urepo.Create(ctx, username, email, "hash", roleID)
		if err != nil {
			t.Fatalf("create %s: %v", username, err)
		}

		return ownedUser{ID: id, Email: email, Role: roleName, RoleID: roleID}
	}
	alice := mustUser("alice", "alice@example.com", 3, "Author")
	bob := mustUser("bob", "bob@example.com", 3, "Author")
	ed := mustUser("ed", "ed@example.com", 2, "Editor")
	carol := mustUser("carol", "carol@example.com", 1, "Admin")
	eve := mustUser("eve", "eve@example.com", 4, "Subscriber")

	// Insert an alice-owned post.
	res, err := db.ExecContext(ctx,
		`INSERT INTO posts (author_id, title, markdown_content, html_content) VALUES (?, 'P', 'p', '<p>p</p>')`,
		alice.ID,
	)
	if err != nil {
		t.Fatalf("insert post: %v", err)
	}

	alicePostID, _ := res.LastInsertId()
	r := chi.NewRouter()
	r.Route("/admin", func(r chi.Router) {
		r.Use(authmw.Authenticate(issuer, nil))
		r.Group(func(r chi.Router) {
			r.Use(rbacmw.Bouncer(rrepo, prepo, srepo, nil))
			r.Post("/posts", okHandler)
			r.Put("/posts/{id}", okHandler)
			r.Delete("/posts/{id}", okHandler)
		})
		// Stand-in route that is NOT in the bouncer matrix and lives outside
		// the bouncer subgroup. Used to verify the bouncer doesn't gate
		// routes it doesn't know about — `/unlisted` is deliberately a path
		// with no production meaning so the assertion can't be confused with a check on a real endpoint's policy.
		r.Get("/unlisted/{id}", okHandler)
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return bouncerEnv{
		srv:         srv,
		issuer:      issuer,
		authorAlice: alice,
		authorBob:   bob,
		editorEd:    ed,
		adminCarol:  carol,
		subEve:      eve,
		alicePostID: alicePostID,
	}
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func tokenFor(t *testing.T, issuer *auth.Issuer, u ownedUser) string {
	t.Helper()
	tok, err := issuer.Access(auth.UserClaim{UserID: u.ID, Email: u.Email, Role: u.Role, RoleID: u.RoleID})
	if err != nil {
		t.Fatalf("issue token for %s: %v", u.Email, err)
	}

	return tok
}

func do(t *testing.T, srv *httptest.Server, method, path, token string) int {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

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

func TestBouncerNoToken(t *testing.T) {
	env := setupBouncerEnv(t)
	if code := do(t, env.srv, http.MethodPost, "/admin/posts", ""); code != http.StatusUnauthorized {
		t.Errorf("missing token: got %d, want 401", code)
	}
}

func TestBouncerAuthorEditsOwnPost(t *testing.T) {
	env := setupBouncerEnv(t)
	tok := tokenFor(t, env.issuer, env.authorAlice)
	path := fmt.Sprintf("/admin/posts/%d", env.alicePostID)
	if code := do(t, env.srv, http.MethodPut, path, tok); code != http.StatusOK {
		t.Errorf("author edits own post: got %d, want 200", code)
	}
}

func TestBouncerAuthorCannotEditOthersPost(t *testing.T) {
	env := setupBouncerEnv(t)
	tok := tokenFor(t, env.issuer, env.authorBob) // bob trying to edit alice's post
	path := fmt.Sprintf("/admin/posts/%d", env.alicePostID)
	if code := do(t, env.srv, http.MethodPut, path, tok); code != http.StatusForbidden {
		t.Errorf("author edits other's post: got %d, want 403", code)
	}
}

func TestBouncerAdminEditsAnyPost(t *testing.T) {
	env := setupBouncerEnv(t)
	tok := tokenFor(t, env.issuer, env.adminCarol)
	path := fmt.Sprintf("/admin/posts/%d", env.alicePostID)
	if code := do(t, env.srv, http.MethodPut, path, tok); code != http.StatusOK {
		t.Errorf("admin edits any post: got %d, want 200", code)
	}
}

func TestBouncerEditorEditsAnyPost(t *testing.T) {
	env := setupBouncerEnv(t)
	tok := tokenFor(t, env.issuer, env.editorEd)
	path := fmt.Sprintf("/admin/posts/%d", env.alicePostID)
	if code := do(t, env.srv, http.MethodPut, path, tok); code != http.StatusOK {
		t.Errorf("editor edits any post: got %d, want 200", code)
	}
}

func TestBouncerSubscriberCannotEdit(t *testing.T) {
	env := setupBouncerEnv(t)
	tok := tokenFor(t, env.issuer, env.subEve)
	path := fmt.Sprintf("/admin/posts/%d", env.alicePostID)
	if code := do(t, env.srv, http.MethodPut, path, tok); code != http.StatusForbidden {
		t.Errorf("subscriber edits post: got %d, want 403", code)
	}
}

// TestBouncerPassesUnlistedRoutes asserts the bouncer is a no-op for routes
// that aren't keyed in its matrix: every authenticated role should reach the
// handler unchanged. This is the contract that lets unrelated routes (/me,  /api/shortlinks list, /admin/post/{id})
// live under /api or /admin without every one needing a matrix entry.
func TestBouncerPassesUnlistedRoutes(t *testing.T) {
	env := setupBouncerEnv(t)
	path := fmt.Sprintf("/admin/unlisted/%d", env.alicePostID)
	for _, u := range []ownedUser{env.authorAlice, env.authorBob, env.adminCarol, env.subEve} {
		tok := tokenFor(t, env.issuer, u)
		if code := do(t, env.srv, http.MethodGet, path, tok); code != http.StatusOK {
			t.Errorf("%s on unlisted route: got %d, want 200", u.Role, code)
		}
	}
}
