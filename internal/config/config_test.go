package config_test

import (
	"testing"

	"github.com/vpramatarov/micro-blog/internal/config"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name     string
		port     string
		dbString string
		wantPort int
		wantDB   string
	}{
		{
			name:     "both env vars set",
			port:     "9000",
			dbString: "/tmp/example.db",
			wantPort: 9000,
			wantDB:   "/tmp/example.db",
		},
		{
			name:     "port empty falls back to default",
			port:     "",
			dbString: "/tmp/example.db",
			wantPort: 8080,
			wantDB:   "/tmp/example.db",
		},
		{
			name:     "port non-numeric falls back to default",
			port:     "not-a-number",
			dbString: "vault.db",
			wantPort: 8080,
			wantDB:   "vault.db",
		},
		{
			name:     "db string empty surfaces as empty",
			port:     "8081",
			dbString: "",
			wantPort: 8081,
			wantDB:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PORT", tt.port)
			t.Setenv("DB_STRING", tt.dbString)

			cfg := config.Load()
			if cfg == nil {
				t.Fatal("Load returned nil")
			}

			if cfg.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", cfg.Port, tt.wantPort)
			}

			if cfg.DB_STRING != tt.wantDB {
				t.Errorf("DB_STRING = %q, want %q", cfg.DB_STRING, tt.wantDB)
			}
		})
	}
}
