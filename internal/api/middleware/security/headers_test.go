package security_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/middleware/security"
)

func TestSecurityHeadersSetOnEveryResponse(t *testing.T) {
	wrapped := security.SecurityHeaders(security.Options{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	// HSTS must be absent in the default configuration
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS unexpectedly set with EnableHSTS=false: %q", got)
	}
}

func TestSecurityHeadersHandlerCanOverrideCSP(t *testing.T) {
	override := "default-src 'self'; script-src https://example.com"
	wrapped := security.SecurityHeaders(security.Options{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

func TestSecurityHeadersAddsHSTSWhenEnabled(t *testing.T) {
	wrapped := security.SecurityHeaders(security.Options{EnableHSTS: true})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	sts := rec.Header().Get("Strict-Transport-Security")

	if got := sts; got != security.HSTSHeaderValue {
		t.Errorf("HSTS: got %q, want %q", got, security.HSTSHeaderValue)
	}

	// HSTS must not carry preload
	if strings.Contains(sts, "preload") {
		t.Errorf("HSTS unexpectedly carries preload directive: %q", sts)
	}

	// check the rest is still present
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("HSTS opt-in should not affect other headers")
	}
}
