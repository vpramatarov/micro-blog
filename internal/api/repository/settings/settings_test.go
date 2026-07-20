package settings_test

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/repository/settings"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestCommentsEnabledSeeded(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := settings.New(db)
	enabled, err := r.CommentsEnabled(t.Context())
	if err != nil {
		t.Fatalf("comments enabled: %v", err)
	}

	if !enabled {
		t.Error("expected the seeded kill-switch to be enabled")
	}
}

func TestSetCommentsEnabledFlips(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := settings.New(db)
	ctx := t.Context()
	if err := r.SetCommentsEnabled(ctx, false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	enabled, err := r.CommentsEnabled(ctx)
	if err != nil {
		t.Fatalf("read after disable: %v", err)
	}

	if enabled {
		t.Error("expected disabled after SetCommentsEnabled(false)")
	}

	if err := r.SetCommentsEnabled(ctx, true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}

	enabled, err = r.CommentsEnabled(ctx)
	if err != nil {
		t.Fatalf("read after re-enable: %v", err)
	}

	if !enabled {
		t.Error("expected enabled after SetCommentsEnabled(true)")
	}
}

func TestSetUpsertsNewKey(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := settings.New(db)
	ctx := t.Context()
	if err := r.Set(ctx, "greeting", "hello"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := r.Set(ctx, "greeting", "hi"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	got, err := r.Get(ctx, "greeting")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got != "hi" {
		t.Errorf("value: got %q, want %q", got, "hi")
	}
}

func TestGetMissingKey(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := settings.New(db)

	if _, err := r.Get(t.Context(), "no-such-key"); !errors.Is(err, settings.ErrSettingNotFound) {
		t.Errorf("got %v, want ErrSettingNotFound", err)
	}
}
