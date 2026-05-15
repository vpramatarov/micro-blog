package auth_test

import (
	"testing"

	"github.com/vpramatarov/micro-blog/internal/auth"
)

func TestPasswordRoundTrip(t *testing.T) {
	testPass := "hunter2!"
	hash, err := auth.Hash(testPass)

	if err != nil {
		t.Fatalf("hash %v", err)
	}

	if hash == testPass {
		t.Fatalf("hash equals plaintext")
	}

	if err := auth.Verify(hash, testPass); err != nil {
		t.Errorf("verify correct password: %v", err)
	}

	if err := auth.Verify(hash, "wrongPass123"); err == nil {
		t.Error("verify wrong password did not fail")
	}
}
