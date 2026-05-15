package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/vpramatarov/micro-blog/internal/auth"
)

func TestIssueAndParseAccessToken(t *testing.T) {
	issuer := auth.NewIssuer("test-secret", 5*time.Minute, auth.IssuerOptions{})
	tok, err := issuer.Access(auth.UserClaim{
		UserID: 42, Email: "a@b.com", Role: "Admin", RoleID: 1,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := issuer.Parse(tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.UserID != 42 || claims.Email != "a@b.com" || claims.Role != "Admin" || claims.RoleID != 1 {
		t.Errorf("claims roundtrip mismatch: %+v", claims)
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
