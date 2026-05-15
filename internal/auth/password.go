package auth

import "golang.org/x/crypto/bcrypt"

// login uses it on the "email not found", so that still does one bcrypt comparisson to runs roughtly the same wall-clock time
// as "email found, wrong password" preventing user-enumeration timing.
var DummyHash = mustHash("dummy__login__timing__parity")

func Hash(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}

	return string(h), nil
}

func Verify(hash, plaintext string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
}

func mustHash(s string) string {
	h, err := Hash(s)
	if err != nil {
		panic("auth: failed to compute DummyHash at init: " + err.Error())
	}

	return h
}
