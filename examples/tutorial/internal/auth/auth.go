// Package auth is pure JWT and password-hashing mechanics — it doesn't know
// what an order is, doesn't touch a database, and doesn't import net/http.
// Looking up a user and deciding whether their password matches is business
// logic and lives in service.AuthService instead; this package only signs,
// verifies, and hashes.
package auth

import (
	"fmt"
	"time"
	"uuid"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type Claims struct {
	UserID   uuid.UUID
	Username string
}

type Issuer struct {
	secret []byte
	expiry time.Duration
}

// Config's generated loader reads JWT_SECRET and JWT_EXPIRY. The secret
// tag is load-bearing: no error path the loader generates ever echoes the
// value it rejected, so a malformed secret cannot leak into a log line.
// The default is validated when `servo generate` runs, not when the
// process starts — a typo'd duration here fails the build.
//
//servo:config prefix=JWT
type Config struct {
	Secret string        `config:"secret,required,secret"`
	Expiry time.Duration `config:"expiry,default=1h"`
}

// minSecretLen is the shortest HMAC key New accepts: 32 bytes, the size of
// HS256's own hash, which is the minimum RFC 7518 §3.2 sets for it.
const minSecretLen = 32

// New refuses a secret too short to sign with. The check lives here, not in
// the config tag: `required` only means JWT_SECRET is set, and an empty value
// is set — an unset Helm value or compose variable arrives exactly like
// that. golang-jwt signs and verifies with an empty key without complaint,
// so without this the service would boot and accept tokens anyone can forge.
// The error gives the length, never the value, for the same reason the tag
// says secret.
func New(cfg Config) (*Issuer, error) {
	if len(cfg.Secret) < minSecretLen {
		return nil, fmt.Errorf("auth: JWT_SECRET is %d bytes; HS256 needs a key of at least %d", len(cfg.Secret), minSecretLen)
	}
	return &Issuer{secret: []byte(cfg.Secret), expiry: cfg.Expiry}, nil
}

type tokenClaims struct {
	UserID   string `json:"uid"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

func (i *Issuer) Issue(userID uuid.UUID, username string) (string, error) {
	now := time.Now()
	claims := tokenClaims{
		UserID:   userID.String(),
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.expiry)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
	if err != nil {
		return "", fmt.Errorf("auth: sign: %w", err)
	}
	return signed, nil
}

func (i *Issuer) Verify(tokenString string) (Claims, error) {
	var claims tokenClaims
	_, err := jwt.ParseWithClaims(tokenString, &claims, func(*jwt.Token) (any, error) {
		return i.secret, nil
	})
	if err != nil {
		return Claims{}, fmt.Errorf("auth: %w", err)
	}

	userID, err := uuid.Parse(claims.UserID)
	if err != nil {
		return Claims{}, fmt.Errorf("auth: invalid user id in token: %w", err)
	}
	return Claims{UserID: userID, Username: claims.Username}, nil
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(hash), nil
}

func CheckPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
