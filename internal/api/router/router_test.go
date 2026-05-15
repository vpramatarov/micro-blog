package router_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterRoutes(t *testing.T) {
	r := buildRouter()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{
			name:       "GET / hits Home",
			method:     http.MethodGet,
			path:       "/",
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET unknown path returns 404",
			method:     http.MethodGet,
			path:       "/does-not-exist",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "POST / returns 405",
			method:     http.MethodPost,
			path:       "/",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			r.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
