package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
)

func TestWriteJSON(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		payload  any
		wantBody string
	}{
		{
			name:     "empty struct",
			status:   http.StatusOK,
			payload:  struct{}{},
			wantBody: "{}\n",
		},
		{
			name:   "struct with field",
			status: http.StatusCreated,
			payload: struct {
				OK bool `json:"ok"`
			}{OK: true},
			wantBody: "{\"ok\":true}\n",
		},
		{
			name:     "nil payload",
			status:   http.StatusOK,
			payload:  nil,
			wantBody: "null\n",
		},
		{
			name:     "slice payload",
			status:   http.StatusAccepted,
			payload:  []string{"a", "b"},
			wantBody: "[\"a\",\"b\"]\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := httpx.WriteJSON(rec, tt.status, tt.payload); err != nil {
				t.Fatalf("WriteJSON returned error: %v", err)
			}

			res := rec.Result()
			t.Cleanup(func() { res.Body.Close() })

			if res.StatusCode != tt.status {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.status)
			}
			if got := res.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want %q", got, "application/json")
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}
