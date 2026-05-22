package tokens_test

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vpramatarov/micro-blog/internal/api/repository/tokens"
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

func TestRefreshTokenInsertFindDelete(t *testing.T) {
	db := testutil.SetupTestDB(t)
	usersRepo := users.New(db)
	r := tokens.New(db)
	ctx := t.Context()

	userID, err := usersRepo.Create(ctx, "rt", "rt@example.com", "h", 4)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	exp := time.Now().Add(time.Hour)
	if err := r.Insert(ctx, userID, "hash-1", exp); err != nil {
		t.Fatalf("insert: %v", err)
	}

	gotUserID, gotExp, err := r.FindOneByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}

	if gotUserID != userID {
		t.Errorf("user_id: got %d, want %d", gotUserID, userID)
	}

	if gotExp.Unix() != exp.UTC().Unix() {
		t.Errorf("expires_at: got %v, want %v", gotExp, exp)
	}

	if err := r.Delete(ctx, "hash-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, _, err := r.FindOneByHash(ctx, "hash-1"); !errors.Is(err, tokens.ErrRefreshTokenNotFound) {
		t.Errorf("after delete: got %v, want ErrRefreshTokenNotFound", err)
	}
}

func TestExpiredRefreshTokensPurgedOnFind(t *testing.T) {
	db := testutil.SetupTestDB(t)
	usersRepo := users.New(db)
	r := tokens.New(db)
	ctx := t.Context()

	userID, err := usersRepo.Create(ctx, "purge", "purge@example.com", "h", 4)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// One expired, one live.
	if err := r.Insert(ctx, userID, "expired", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("insert expired: %v", err)
	}

	if err := r.Insert(ctx, userID, "live", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("insert live: %v", err)
	}

	// Find against the live token; the side-effect purge should sweep the expired row.
	if _, _, err := r.FindOneByHash(ctx, "live"); err != nil {
		t.Fatalf("find live: %v", err)
	}

	if _, _, err := r.FindOneByHash(ctx, "expired"); !errors.Is(err, tokens.ErrRefreshTokenNotFound) {
		t.Errorf("expected expired row purged, got err=%v", err)
	}
}

func TestRefreshTokenCascadeOnUserDelete(t *testing.T) {
	db := testutil.SetupTestDB(t)
	usersRepo := users.New(db)
	r := tokens.New(db)
	ctx := t.Context()

	userID, err := usersRepo.Create(ctx, "cascade", "cascade@example.com", "h", 4)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := r.Insert(ctx, userID, "hash-2", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if _, _, err := r.FindOneByHash(ctx, "hash-2"); !errors.Is(err, tokens.ErrRefreshTokenNotFound) {
		t.Errorf("expected cascade delete to remove refresh token, got %v", err)
	}
}

func TestRotateRefreshTokenAtomicSwap(t *testing.T) {
	db := testutil.SetupTestDB(t)
	userRepo := users.New(db)
	r := tokens.New(db)
	ctx := t.Context()
	userID, err := userRepo.Create(ctx, "rot", "rot@example.com", "h", 4)
	if err != nil {
		t.Fatalf("create user : %v", err)
	}

	if err := r.Insert(ctx, userID, "old-hash", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed old: %v", err)
	}

	newExpiresAt := time.Now().Add(2 * time.Hour)
	if err := r.RotateRefreshToken(ctx, "old-hash", userID, "new-hash", newExpiresAt); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if _, _, err := r.FindOneByHash(ctx, "old-hash"); !errors.Is(err, tokens.ErrRefreshTokenNotFound) {
		t.Errorf("old hash: go %v, want ErrRefreshTokenNotFound", err)
	}

	gotUserID, gotExpire, err := r.FindOneByHash(ctx, "new-hash")
	if err != nil {
		t.Fatalf("find new: %v", err)
	}

	if gotUserID != userID {
		t.Errorf("new hash user_id: got %d, want %d", gotUserID, userID)
	}

	if gotExpire.Unix() != newExpiresAt.UTC().Unix() {
		t.Errorf("new hash expires_at: got %v, want %v", gotExpire, newExpiresAt)
	}
}

func TestRotateRefreshTokenReturnsNotFoundWhenAlreadyRotated(t *testing.T) {
	db := testutil.SetupTestDB(t)
	userRepo := users.New(db)
	r := tokens.New(db)
	ctx := t.Context()
	userID, err := userRepo.Create(ctx, "race", "race@example.com", "h", 4)
	if err != nil {
		t.Fatalf("create user : %v", err)
	}

	if err := r.Insert(ctx, userID, "stale-hash", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed old: %v", err)
	}

	// First rotation wins.
	if err := r.RotateRefreshToken(ctx, "stale-hash", userID, "winner", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("first rotate: %v", err)
	}

	// Second rotation against the same now-removed old hash must reject and MUST NOT insert "loser"
	err = r.RotateRefreshToken(ctx, "stale-hash", userID, "loser", time.Now().Add(time.Hour))
	if !errors.Is(err, tokens.ErrRefreshTokenNotFound) {
		t.Fatalf("second rotate: got %v, want ErrRefreshTokenNotFound", err)
	}

	if _, _, err := r.FindOneByHash(ctx, "loser"); !errors.Is(err, tokens.ErrRefreshTokenNotFound) {
		t.Errorf("loser hash unexpectedly inserted: err=%v", err)
	}
}

func TestRevokeJTIInsertAndCheck(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tokens.New(db)
	ctx := t.Context()
	revoked, err := r.IsJTIRevoked(ctx, "never-revoked")
	jti := "abc-123"
	if err != nil {
		t.Fatalf("pre-revoke check: %v", err)
	}

	if revoked {
		t.Errorf("unrevoked jti reported revoked")
	}

	if err := r.RevokeJTI(ctx, jti, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	revoked, err = r.IsJTIRevoked(ctx, jti)
	if err != nil {
		t.Fatalf("post-revoke check: %v", err)
	}

	if !revoked {
		t.Errorf("revoked jti reported not revoked")
	}

	// Double revoke is idempotent - INSERT OR IGNORE
	if err := r.RevokeJTI(ctx, jti, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("re-revoke should be a no-op, got: %v", err)
	}
}

func TestRevokeJTISweepsExpiredOnInsert(t *testing.T) {
	db := testutil.SetupTestDB(t)
	r := tokens.New(db)
	ctx := t.Context()
	// Seed one expired and one live revocation
	if err := r.RevokeJTI(ctx, "live", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed live: %v", err)
	}

	q := fmt.Sprintf("INSERT INTO %s (jti, exp) VALUES (?, ?)", tokens.REVOKED_JITS_TABLE)
	if _, err := db.ExecContext(ctx, q, "stale", time.Now().Add(-time.Hour).UTC()); err != nil {
		t.Fatalf("seed stale: %v", err)
	}

	// Any subsequen RevokeJTI triggers the sweep
	if err := r.RevokeJTI(ctx, "fresh", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("trigger sweep: %v", err)
	}

	var staleCount int
	q = fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE jti = 'stale'`, tokens.REVOKED_JITS_TABLE)
	if err := db.QueryRowContext(ctx, q).Scan(&staleCount); err != nil {
		t.Fatalf("count stale: %v", err)
	}

	if staleCount != 0 {
		t.Errorf("expired row should have been swept, got count=%d", staleCount)
	}

	// The live rows must still be present
	revoked, err := r.IsJTIRevoked(ctx, "live")
	if err != nil || !revoked {
		t.Fatalf("live row swept by mistake; revoked=%v, err=%v", revoked, err)
	}

	revoked, err = r.IsJTIRevoked(ctx, "fresh")
	if err != nil {
		t.Fatalf("fresh row missing; revoked=%v, err=%v", revoked, err)
	}
}
