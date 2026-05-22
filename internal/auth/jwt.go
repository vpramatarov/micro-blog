package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID int64  `json:"uid"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	RoleID int64  `json:"rid"`
	jwt.RegisteredClaims
}

type UserClaim struct {
	UserID int64
	Email  string
	Role   string
	RoleID int64
}

// IssuerOptions carries optional `iss` and `aud` claims. Leave the fields empty
// in tests to skip iss/aud verification; production wiring always sets both.
type IssuerOptions struct {
	Issuer   string
	Audience string
}

type Issuer struct {
	secret    []byte
	accessTTL time.Duration
	opts      IssuerOptions
}

func NewIssuer(secret string, accessTTL time.Duration, opts IssuerOptions) *Issuer {
	return &Issuer{secret: []byte(secret), accessTTL: accessTTL, opts: opts}
}

func (i *Issuer) Access(u UserClaim) (string, error) {
	now := time.Now()
	jti, err := NewJTI()
	if err != nil {
		return "", fmt.Errorf("generate jti: %v", err)
	}

	reg := jwt.RegisteredClaims{
		ID:        jti,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(i.accessTTL)),
		Subject:   fmt.Sprintf("%d", u.UserID),
	}

	if i.opts.Issuer != "" {
		reg.Issuer = i.opts.Issuer
	}

	if i.opts.Audience != "" {
		reg.Audience = jwt.ClaimStrings{i.opts.Audience}
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID:           u.UserID,
		Email:            u.Email,
		Role:             u.Role,
		RoleID:           u.RoleID,
		RegisteredClaims: reg,
	})
	return token.SignedString(i.secret)
}

// returns fresh unique JWT identifier formatted as an RFC4122 UUID v4 string.
func NewJTI() (string, error) {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", err
	}

	buffer[6] = (buffer[6] & 0x0f) | 0x40 // version 4
	buffer[8] = (buffer[8] & 0x3f) | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(buffer[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

func (i *Issuer) Parse(token string) (*Claims, error) {
	parseOpts := []jwt.ParserOption{}
	if i.opts.Issuer != "" {
		parseOpts = append(parseOpts, jwt.WithIssuer(i.opts.Issuer))
	}

	if i.opts.Audience != "" {
		parseOpts = append(parseOpts, jwt.WithAudience(i.opts.Audience))
	}

	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}

		return i.secret, nil
	}, parseOpts...)

	if err != nil {
		return nil, err
	}

	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid token")
	}

	return claims, nil
}

// NewRefreshToken returns a fresh opaque token (base64url, 32 random bytes) and the sha256 hex hash that should be persisted server-side.
func NewRefreshToken() (plaintext, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}

	plaintext = base64.RawURLEncoding.EncodeToString(buf)
	return plaintext, HashRefreshToken(plaintext), nil
}

func HashRefreshToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
