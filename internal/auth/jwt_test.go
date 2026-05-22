package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/vpramatarov/micro-blog/internal/auth"
)

func TestIssueAndParseAccessToken(t *testing.T) {
	issuer := auth.NewIssuer("test-secret", 5*time.Minute, auth.IssuerOptions{})
	token, err := issuer.Access(auth.UserClaim{
		UserID: 42, Email: "a@b.com", Role: "Admin", RoleID: 1,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	claims, err := issuer.Parse(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if claims.UserID != 42 || claims.Email != "a@b.com" || claims.Role != "Admin" || claims.RoleID != 1 {
		t.Errorf("claims roundtrip mismatch: %+v", claims)
	}

	// JTI must be present so the Logout-driven revocation table has a stable unique key per access token.
	// Two tokens issued from the same issuer must NOT share the same JTI.
	if claims.ID == "" {
		t.Errorf("expected non-empty jti on issued access token")
	}

	token2, err := issuer.Access(auth.UserClaim{UserID: 42, Email: "test@example.com", Role: "Admin", RoleID: 1})
	if err != nil {
		t.Fatalf("issue second: %v", err)
	}

	claims2, err := issuer.Parse(token2)
	if err != nil {
		t.Fatalf("parse second: %v", err)
	}

	if claims2.ID == claims.ID {
		t.Errorf("two tokens unexpectedly share jti=%q", claims.ID)
	}
}

func TestParseRejectsExpired(t *testing.T) {
	issuer := auth.NewIssuer("test-secret", -1*time.Second, auth.IssuerOptions{}) // already expired
	tok, err := issuer.Access(auth.UserClaim{UserID: 1, Email: "x", Role: "Admin", RoleID: 1})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	if _, err := issuer.Parse(tok); err == nil {
		t.Error("expected expired token to be rejected")
	}
}

func TestParseRejectsBadSignature(t *testing.T) {
	a := auth.NewIssuer("secret-a", time.Minute, auth.IssuerOptions{})
	b := auth.NewIssuer("secret-b", time.Minute, auth.IssuerOptions{})
	tok, err := a.Access(auth.UserClaim{UserID: 1, Email: "x", Role: "Admin", RoleID: 1})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	if _, err := b.Parse(tok); err == nil {
		t.Error("expected token signed with different secret to be rejected")
	}
}

func TestRefreshTokenHashIsDeterministic(t *testing.T) {
	plain, hash, err := auth.NewRefreshToken()
	if err != nil {
		t.Fatalf("new refresh token: %v", err)
	}

	if plain == "" || hash == "" {
		t.Fatal("empty token or hash")
	}

	if strings.Contains(plain, hash) {
		t.Fatal("hash appears in plaintext — not expected")
	}

	if auth.HashRefreshToken(plain) != hash {
		t.Error("hash function is not deterministic")
	}
}
