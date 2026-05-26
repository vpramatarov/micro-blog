package ratelimit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vpramatarov/micro-blog/internal/api/middleware/ratelimit"
)

func TestPerIPAllowsBurstThenBlocks(t *testing.T) {
	mw := ratelimit.PerIP(ratelimit.RateLimitConfig{
		RPS:   ratelimit.Per(5, time.Minute),
		Burst: 5,
	}, nil)

	wrapped := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := range 5 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		wrapped.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("burst request %d: got %d, want 200", i+1, rec.Code)
		}
	}

	// 6th request from the same ip within the window: blocked.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("post-burst request: got %d, want 429", rec.Code)
	}

	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After: got %q, want 60", got)
	}
}

func TestPerIpIsolatesPeerIps(t *testing.T) {
	mw := ratelimit.PerIP(ratelimit.RateLimitConfig{
		RPS:   ratelimit.Per(2, time.Minute),
		Burst: 2,
	}, nil)

	wrapped := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// IP A drains their burst
	for i := range 2 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		wrapped.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("burst request %d: got %d, want 200", i+1, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("IP A post-burst: got %d, want 429", rec.Code)
	}

	// IP B still has full burst
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = "192.168.1.2:1234"
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("IP B first request: got %d, want 200 (peer IP should not be limited)", rec.Code)
	}
}

func TestPerHelper(t *testing.T) {
	got := ratelimit.Per(60, time.Minute)
	if float64(got) != 1.0 {
		t.Errorf("Per(60, 1min): got %v, want 1 token/sec", got)
	}

	got = ratelimit.Per(5, time.Minute)
	if got <= 0 || got >= 0.1 {
		t.Errorf("Per(60, 1min): got %v, want roughly 0.083", got)
	}

	// Edge: zero events or zero duration yields zero (effectively closed).
	if got := ratelimit.Per(0, time.Minute); got != 0 {
		t.Errorf("Per(0, 1min): got %v, want 0", got)
	}

	if got := ratelimit.Per(5, 0); got != 0 {
		t.Errorf("Per(5, 0): got %v, want 0", got)
	}
}
