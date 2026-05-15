package users_test

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/testutil"
)

func TestMain(m *testing.M) {
	if err := testutil.EnsureTestSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare test schema: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestCreateAndGetUser(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := users.New(db)
	ctx := t.Context()

	id, err := r.Create(ctx, "alice", "alice@example.com", "hash", 4)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if id == 0 {
		t.Fatal("zero id returned")
	}

	got, err := r.GetByEmail(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("get by email: %v", err)
	}

	if got.ID != id || got.Username != "alice" || got.RoleName != "Subscriber" || got.RoleID != 4 {
		t.Errorf("unexpected user: %+v", got)
	}

	byID, err := r.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}

	if byID.Email != "alice@example.com" {
		t.Errorf("got email %q", byID.Email)
	}
}

func TestCreateUserDuplicateEmail(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := users.New(db)
	ctx := t.Context()

	if _, err := r.Create(ctx, "a", "dup@example.com", "h", 4); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := r.Create(ctx, "b", "dup@example.com", "h", 4)
	if !errors.Is(err, users.ErrUserDuplicate) {
		t.Errorf("got %v, want ErrUserDuplicate", err)
	}
}

func TestGetUserByEmailNotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := users.New(db)
	if _, err := r.GetByEmail(t.Context(), "missing@example.com"); !errors.Is(err, users.ErrUserNotFound) {
		t.Errorf("got %v, want ErrUserNotFound", err)
	}
}
