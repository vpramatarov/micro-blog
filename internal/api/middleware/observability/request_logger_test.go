package observability_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/middleware/observability"
)

func TestRequestLoggerEmitsStructuredLine(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	h := observability.RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hi"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/foo", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("decode log line: %v; raw=%s", err, buf.String())
	}

	if entry["msg"] != "http_request" {
		t.Errorf("msg: got %v, want http_request", entry["msg"])
	}
	if entry["method"] != "POST" {
		t.Errorf("method: got %v, want POST", entry["method"])
	}
	if entry["path"] != "/foo" {
		t.Errorf("path: got %v, want /foo", entry["path"])
	}
	if entry["status"].(float64) != float64(http.StatusTeapot) {
		t.Errorf("status: got %v, want %d", entry["status"], http.StatusTeapot)
	}
	if entry["bytes"].(float64) != 2 {
		t.Errorf("bytes: got %v, want 2", entry["bytes"])
	}
	if entry["remote_addr"] != "10.0.0.1:1234" {
		t.Errorf("remote_addr: got %v", entry["remote_addr"])
	}
	if _, ok := entry["duration_ms"]; !ok {
		t.Error("duration_ms missing")
	}
}

func TestRequestLoggerCapturesDefaultStatus(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	// Handler that writes the body but never explicitly calls WriteHeader — the wrapper should report 200 (Go's implicit default).
	h := observability.RequestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/bar", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var entry map[string]any
	_ = json.Unmarshal(buf.Bytes(), &entry)
	if entry["status"].(float64) != float64(http.StatusOK) {
		t.Errorf("default status: got %v, want 200", entry["status"])
	}
}
