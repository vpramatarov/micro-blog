package router_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHome covers the welcome page now inlined in routes.go. The drift test's
// buildRouter helper already wires the full chi mux with nil deps, so we reuse it here.
func TestHome(t *testing.T) {
	r := buildRouter()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	res := rec.Result()
	t.Cleanup(func() { res.Body.Close() })

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}

	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	if got := rec.Body.String(); got != "{}\n" {
		t.Errorf("body = %q, want %q", got, "{}\n")
	}
}
