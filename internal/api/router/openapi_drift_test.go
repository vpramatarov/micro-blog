package router_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/yaml"

	"github.com/vpramatarov/micro-blog/api"
	authh "github.com/vpramatarov/micro-blog/internal/api/handlers/auth"
	categoriesh "github.com/vpramatarov/micro-blog/internal/api/handlers/categories"
	docsh "github.com/vpramatarov/micro-blog/internal/api/handlers/docs"
	postsh "github.com/vpramatarov/micro-blog/internal/api/handlers/posts"
	shortlinksh "github.com/vpramatarov/micro-blog/internal/api/handlers/shortlinks"
	tagsh "github.com/vpramatarov/micro-blog/internal/api/handlers/tags"
	usersh "github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	"github.com/vpramatarov/micro-blog/internal/api/router"
	"github.com/vpramatarov/micro-blog/internal/config"
)

// buildRouter constructs the real chi tree with nil deps everywhere — we only
// need the routing topology, not the ability to handle requests. After the
// handlers/repository split, no single constructor wires everything; the
// drift test recreates the same wiring main.go does in miniature.
func buildRouter() *chi.Mux {
	authSvc := authh.New(&config.Config{}, nil, nil, nil, nil)
	usersSvc := usersh.New(&config.Config{}, nil, nil, nil)
	postsSvc := postsh.New(nil, nil, nil, nil, nil)
	shortlinksSvc := shortlinksh.New(nil, nil, nil)
	docsSvc := docsh.New(nil, nil)
	categoriesSvc := categoriesh.New(nil, nil)
	tagsSvc := tagsh.New(nil, nil)
	return router.New(
		router.Services{
			Auth: authSvc, Users: usersSvc, Posts: postsSvc,
			ShortLinks: shortlinksSvc, Docs: docsSvc,
			Categories: categoriesSvc, Tags: tagsSvc,
		},
		router.Middlewares{},
	)
}

// TestOpenAPISpecCoversEveryRoute is a drift guard: every (method, pattern)
// registered in the chi tree must appear in api/openapi.yaml. Adding a new
// route without a matching spec entry fails this test, which is the whole reason hand-writing the spec is viable.
//
// The reverse direction (spec entries with no matching route) is intentionally
// not asserted — the spec can legitimately describe deprecated paths during a transition.
func TestOpenAPISpecCoversEveryRoute(t *testing.T) {
	r := buildRouter()

	// Parse the embedded spec into a minimal shape: paths -> method -> _.
	// sigs.k8s.io/yaml routes YAML through JSON, so struct tags use json:.
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := yaml.Unmarshal(api.Spec, &doc); err != nil {
		t.Fatalf("parse embedded openapi.yaml: %v", err)
	}

	// Methods openapi cares about. chi.Walk reports each registered method
	// individually, so this list is also the lowercase keys we expect in the spec's paths.<path> map.
	validMethods := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true, "delete": true,
		"head": true, "options": true, "trace": true,
	}

	walkErr := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		lower := strings.ToLower(method)
		if !validMethods[lower] {
			return nil
		}

		ops, ok := doc.Paths[route]
		if !ok {
			t.Errorf("route %s %s: path missing from openapi.yaml", method, route)
			return nil
		}

		if _, ok := ops[lower]; !ok {
			t.Errorf("route %s %s: method missing on path entry in openapi.yaml", method, route)
		}

		return nil
	})

	if walkErr != nil {
		t.Fatalf("chi.Walk: %v", walkErr)
	}
}

// TestOpenAPIOperationsHaveValidRoles asserts every operation in the spec is
// annotated with x-roles and that every value is a known audience. Without
// the annotation the role-filter falls open (anyone sees the op), so this
// test prevents accidental "default visible to everyone" regressions.
func TestOpenAPIOperationsHaveValidRoles(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := yaml.Unmarshal(api.Spec, &doc); err != nil {
		t.Fatalf("parse embedded openapi.yaml: %v", err)
	}

	validMethods := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true, "delete": true,
		"head": true, "options": true, "trace": true,
	}
	validAudiences := map[string]bool{}
	for _, a := range api.Audiences {
		validAudiences[a] = true
	}

	for path, ops := range doc.Paths {
		for method, raw := range ops {
			if !validMethods[strings.ToLower(method)] {
				continue
			}

			var op struct {
				XRoles []string `json:"x-roles"`
			}
			if err := json.Unmarshal(raw, &op); err != nil {
				t.Errorf("decode %s %s: %v", strings.ToUpper(method), path, err)
				continue
			}

			if len(op.XRoles) == 0 {
				t.Errorf("%s %s: missing x-roles", strings.ToUpper(method), path)
				continue
			}

			for _, r := range op.XRoles {
				if !validAudiences[r] {
					t.Errorf("%s %s: unknown audience %q in x-roles", strings.ToUpper(method), path, r)
				}
			}
		}
	}
}

// TestOpenAPIFilteredVariantsMatchExpected pins which operations are visible
// to each audience. Adding a route with role-restricted access requires
// updating both the spec annotation and this expectation table — keeping the
// role classification a deliberate decision instead of a default-fallthrough.
func TestOpenAPIFilteredVariantsMatchExpected(t *testing.T) {
	// Operations everyone can see (public + cookie-auth + docs).
	public := []string{
		"GET /",
		"GET /posts",
		"GET /posts/{slug}",
		"GET /p/{code}",
		"GET /categories",
		"GET /tags",
		"GET /s/{code}",
		"GET /openapi.yaml",
		"GET /openapi.json",
		"GET /docs",
		"POST /auth/register",
		"POST /auth/login",
		"POST /auth/refresh",
		"POST /auth/logout",
	}
	// Plus authenticated-any-role (profile + handler-filtered lists).
	anyAuth := append(append([]string{}, public...),
		"GET /api/me",
		"PUT /api/me",
		"GET /api/shortlinks",
		"GET /admin/posts",
	)
	// Plus post + shortlink writes — Authors and above. (Author and Editor
	// used to share a tier; they diverge here because category/tag writes are Editor-and-above only.)
	author := append(append([]string{}, anyAuth...),
		"POST /api/shortlinks",
		"PUT /api/shortlinks/{id}",
		"DELETE /api/shortlinks/{id}",
		"POST /admin/posts",
		"PUT /admin/posts/{id}",
		"DELETE /admin/posts/{id}",
	)
	// Plus taxonomy writes (Editor + Admin only).
	editor := append(append([]string{}, author...),
		"POST /admin/categories",
		"PUT /admin/categories/{id}",
		"DELETE /admin/categories/{id}",
		"POST /admin/tags",
		"PUT /admin/tags/{id}",
		"DELETE /admin/tags/{id}",
	)
	// Plus admin-only (numeric-id post read + user CRUD).
	admin := append(append([]string{}, editor...),
		"GET /admin/post/{id}",
		"GET /admin/users",
		"POST /admin/users",
		"GET /admin/users/{id}",
		"PUT /admin/users/{id}",
		"DELETE /admin/users/{id}",
	)

	expected := map[string][]string{
		"anonymous":  public,
		"Subscriber": anyAuth,
		"Author":     author,
		"Editor":     editor,
		"Admin":      admin,
	}

	for audience, want := range expected {
		t.Run(audience, func(t *testing.T) {
			got := operationsIn(t, api.SpecJSONByRole[audience])
			compareOperationSets(t, audience, got, want)
		})
	}
}

func operationsIn(t *testing.T, spec []byte) map[string]bool {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("parse filtered spec: %v", err)
	}

	validMethods := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true, "delete": true,
		"head": true, "options": true, "trace": true,
	}

	out := map[string]bool{}
	for path, ops := range doc.Paths {
		for method := range ops {
			if validMethods[strings.ToLower(method)] {
				out[strings.ToUpper(method)+" "+path] = true
			}
		}
	}

	return out
}

func compareOperationSets(t *testing.T, audience string, got map[string]bool, want []string) {
	t.Helper()
	wantSet := map[string]bool{}
	for _, op := range want {
		wantSet[op] = true
		if !got[op] {
			t.Errorf("%s spec missing expected op %q", audience, op)
		}
	}

	for op := range got {
		if !wantSet[op] {
			t.Errorf("%s spec contains unexpected op %q", audience, op)
		}
	}
}
