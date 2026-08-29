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

	"example.com/servoorders/internal/config"
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

const envPrefix = "JWT_"

type Config struct {
	Secret string        `env:"SECRET,required"`
	Expiry time.Duration `env:"EXPIRY" envDefault:"1h"`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, envPrefix)
}

func New(cfg *Config) *Issuer {
	return &Issuer{secret: []byte(cfg.Secret), expiry: cfg.Expiry}
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
