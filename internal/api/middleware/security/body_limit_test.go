package security_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/middleware/security"
)

func TestLimitBodyAllowsUpToCap(t *testing.T) {
	const cap = 10
	srv := buildLimitedServer(t, cap)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789")) // exactly 10
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("at-cap body: got %d, want 200", rec.Code)
	}
}

func TestLimitBodyRejectsOverCap(t *testing.T) {
	const cap = 10
	srv := buildLimitedServer(t, cap)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("this body is longer than ten bytes"))
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("over-cap body: got %d, want 400", rec.Code)
	}
}

// buildLimitedServer mounts LimitBody(cap) and a handler that drains r.Body — if reading fails,
// the handler writes a 400, mirroring how the real json decoder reacts to *http.MaxBytesError.
func buildLimitedServer(t *testing.T, cap int64) http.Handler {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "invalid_body", http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)
	})

	return security.LimitBody(cap)(h)
}
