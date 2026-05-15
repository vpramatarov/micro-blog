package security_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/middleware/security"
)

func TestSecurityHeadersSetOnEveryResponse(t *testing.T) {
	wrapped := security.SecurityHeaders()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	wrapped.ServeHTTP(rec, req)

	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s: got %q, want %q", k, got, v)
		}
	}
}

func TestSecurityHeadersHandlerCanOverrideCSP(t *testing.T) {
	override := "default-src 'self'; script-src https://example.com"
	wrapped := security.SecurityHeaders()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", override)
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))

	if got := rec.Header().Get("Content-Security-Policy"); got != override {
		t.Errorf("CSP override: got %q, want %q", got, override)
	}
	// Other headers should still be present.
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("override should not clear other headers")
	}
}
