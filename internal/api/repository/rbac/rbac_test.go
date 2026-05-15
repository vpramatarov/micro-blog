package rbac_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// Asserts the seeded matrix from migration 00004:
//
//	Admin (1)      -> post:edit = all
//	Editor (2)     -> post:edit = all
//	Author (3)     -> post:edit = own
//	Subscriber (4) -> post:edit = "" (not granted)
func TestGetRolePermissionScope(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := rbac.New(db)
	ctx := t.Context()

	tests := []struct {
		role int64
		perm string
		want string
	}{
		{1, "post:edit", "all"},
		{2, "post:edit", "all"},
		{3, "post:edit", "own"},
		{4, "post:edit", ""},
		{1, "ghost:perm", ""},
	}
	for _, tt := range tests {
		got, err := r.GetRolePermissionScope(ctx, tt.role, tt.perm)
		if err != nil {
			t.Errorf("role %d / %q: %v", tt.role, tt.perm, err)
			continue
		}

		if got != tt.want {
			t.Errorf("role %d / %q: got %q, want %q", tt.role, tt.perm, got, tt.want)
		}
	}
}
