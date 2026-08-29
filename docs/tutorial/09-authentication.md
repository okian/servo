# 9. Authentication

Every order endpoint needs to know who's asking, and reject requests that don't say. This chapter
builds that in two pieces, deliberately kept separate: the JWT and password mechanics (this
package doesn't know what an order is, doesn't touch a database, and doesn't import `net/http`),
and the business logic of an actual login (which does — it looks up a real user and checks a real
password), which belongs in the service layer alongside everything else in
[chapter 8](08-service-layer.md).

## Build the JWT mechanics

Create `auth/auth.go`. Start with what a verified token tells the rest of the service:

```go
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
```

`Issuer` has no lifecycle capabilities — no `Init`, no `Stop`, no `Health`. It's pure logic, same
as `OrderService` from the last chapter: nothing about signing or verifying a token needs a
connection to anything.

Now the actual token shape. `jwt.RegisteredClaims` supplies the standard fields (issued-at,
expiry); embed it alongside the two fields this service actually cares about:

```go
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
```

`HS256` means the same secret both signs and verifies — simplest option, and the right one as long
as exactly one service issues and checks these tokens. The moment a second service needs to verify
tokens without also being trusted to *issue* them, that symmetry becomes a liability; see
[chapter 21](21-alternatives-and-further-reading.md#jwt-signing-algorithms) for what changes then.

`Verify` is the reverse direction, and needs to turn parsing failures — an expired token, a bad
signature, garbage input — into one clear error rather than distinguishing every case:

```go
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
```

Finally, the password side — `HashPassword` for turning a plaintext password into something safe
to store, `CheckPassword` for comparing one against a stored hash:

```go
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
```

bcrypt, not a bare SHA-256 hash of the password: it's deliberately slow (tunable via
`bcrypt.DefaultCost`), which is exactly the property you want for password storage — slow enough
that brute-forcing a stolen hash is impractical, fast enough that one real login isn't
noticeable. Never write a password-hashing function yourself; this is the one piece of
cryptography in this tutorial where "just use the standard library everyone already trusts"
isn't a compromise, it's the only correct answer.

## Prove the mechanics work in isolation

```go
func TestIssueThenVerifyRoundTrips(t *testing.T) {
	issuer := auth.New(&Config{Secret: "test-secret", Expiry: time.Hour})
	userID := uuid.New()

	token, err := issuer.Issue(userID, "alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	claims, err := issuer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != userID || claims.Username != "alice" {
		t.Errorf("Verify returned %+v, want UserID=%s Username=alice", claims, userID)
	}
}
```

The interesting tests are the rejection paths — an expired token, and one signed with a different
secret than the one verifying it:

```go
func TestVerifyRejectsAnExpiredToken(t *testing.T) {
	issuer := auth.New(&Config{Secret: "test-secret", Expiry: -time.Hour}) // already expired
	token, err := issuer.Issue(uuid.New(), "alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if _, err := issuer.Verify(token); err == nil {
		t.Fatal("expected Verify to reject an expired token")
	}
}

func TestVerifyRejectsATokenSignedWithADifferentSecret(t *testing.T) {
	issuerA := auth.New(&Config{Secret: "secret-a", Expiry: time.Hour})
	issuerB := auth.New(&Config{Secret: "secret-b", Expiry: time.Hour})

	token, err := issuerA.Issue(uuid.New(), "alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := issuerB.Verify(token); err == nil {
		t.Fatal("expected Verify to reject a token signed with a different secret")
	}
}
```

A negative `Expiry` is a neat trick worth remembering: it produces an already-expired token
without needing to sleep in the test or fake the clock. Run all four:

```
$ go test ./auth/... -v
=== RUN   TestIssueThenVerifyRoundTrips
--- PASS: TestIssueThenVerifyRoundTrips (0.00s)
=== RUN   TestVerifyRejectsAnExpiredToken
--- PASS: TestVerifyRejectsAnExpiredToken (0.00s)
=== RUN   TestVerifyRejectsATokenSignedWithADifferentSecret
--- PASS: TestVerifyRejectsATokenSignedWithADifferentSecret (0.00s)
=== RUN   TestHashPasswordThenCheckPasswordRoundTrips
--- PASS: TestHashPasswordThenCheckPasswordRoundTrips (0.17s)
PASS
ok  	example.com/servoorders/internal/auth	0.496s
```

## Add the domain error a failed login needs

A wrong password and an unknown username are two different situations internally, but they need
to produce the *same* response — otherwise the login endpoint becomes a way to enumerate which
usernames exist. Go back to `domain/domain.go` and add one more sentinel error alongside the three
from [chapter 4](04-domain-layer.md):

```go
var (
	ErrNotFound           = errors.New("resource not found")
	ErrForbidden          = errors.New("access forbidden")
	ErrValidation         = errors.New("validation failed")
	ErrInvalidCredentials = errors.New("invalid credentials")
)
```

## Write the login business logic

This is where `auth`'s mechanics meet a real user lookup — business logic, so it belongs in
`service`, not `auth`. Create `service/auth_service.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"

	"example.com/servoorders/internal/auth"
	"example.com/servoorders/internal/domain"
	"example.com/servoorders/internal/repository"
)

type AuthService struct {
	users  repository.UserRepository
	issuer *auth.Issuer
}

func NewAuthService(users repository.UserRepository, issuer *auth.Issuer) *AuthService {
	return &AuthService{users: users, issuer: issuer}
}
```

Now `Login`, where the "don't leak which usernames exist" rule actually gets enforced:

```go
func (s *AuthService) Login(ctx context.Context, username, password string) (string, error) {
	user, err := s.users.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return "", domain.ErrInvalidCredentials
		}
		return "", fmt.Errorf("service: login: %w", err)
	}

	if err := auth.CheckPassword(user.PasswordHash, password); err != nil {
		return "", domain.ErrInvalidCredentials
	}

	token, err := s.issuer.Issue(user.ID, user.Username)
	if err != nil {
		return "", fmt.Errorf("service: login: %w", err)
	}
	return token, nil
}
```

Both failure paths — no such user, and a user that exists but typed the wrong password — return
the exact same `domain.ErrInvalidCredentials`. A client (or an attacker scripting login attempts)
gets identical responses either way.

## Test it with a mocked repository, a real Issuer

```go
func TestLoginSucceedsWithCorrectPassword(t *testing.T) {
	ctrl := gomock.NewController(t)
	users := mocks.NewMockUserRepository(ctrl)
	issuer := auth.New(&Config{Secret: "test-secret", Expiry: time.Hour})

	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	userID := uuid.New()
	users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(&domain.User{ID: userID, Username: "alice", PasswordHash: hash}, nil)

	authSvc := service.NewAuthService(users, issuer)
	token, err := authSvc.Login(context.Background(), "alice", "password123")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	claims, err := issuer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != userID {
		t.Errorf("token UserID = %s, want %s", claims.UserID, userID)
	}
}
```

Notice this test uses a *real* `auth.Issuer`, not a mock — signing and verifying a token is cheap,
deterministic, and has no external dependency, so mocking it would only add indirection for no
benefit. The mock is reserved for `UserRepository`, the one thing that would otherwise need a real
database. The other two tests — wrong password, and an unknown username — both assert the same
outcome, proving the two failure paths really are indistinguishable from the outside:

```go
func TestLoginFailsWithUnknownUsernameTheSameWayAsWrongPassword(t *testing.T) {
	ctrl := gomock.NewController(t)
	users := mocks.NewMockUserRepository(ctrl)
	issuer := auth.New(&Config{Secret: "test-secret", Expiry: time.Hour})

	users.EXPECT().GetByUsername(gomock.Any(), "nobody").Return(nil, domain.ErrNotFound)

	authSvc := service.NewAuthService(users, issuer)
	if _, err := authSvc.Login(context.Background(), "nobody", "password123"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Login for an unknown username: err = %v, want domain.ErrInvalidCredentials", err)
	}
}
```

```
$ go test ./service/... -run TestLogin -v
=== RUN   TestLoginSucceedsWithCorrectPassword
--- PASS: TestLoginSucceedsWithCorrectPassword (0.13s)
=== RUN   TestLoginFailsWithWrongPassword
--- PASS: TestLoginFailsWithWrongPassword (0.10s)
=== RUN   TestLoginFailsWithUnknownUsernameTheSameWayAsWrongPassword
--- PASS: TestLoginFailsWithUnknownUsernameTheSameWayAsWrongPassword (0.00s)
PASS
ok  	example.com/servoorders/internal/service	0.546s
```

The middleware that actually extracts a Bearer token from a request and calls `Verify` on it
belongs to the HTTP layer, not here — that's [chapter 10](10-api-layer.md), next.

## Diagnostics

- **`auth: token is expired`** (or similar, from `jwt.ParseWithClaims`) — exactly the intended
  behavior once `Expiry` has elapsed. A client should re-login, not treat this as a server error.
- **`auth: signature is invalid`** — either `JWT_SECRET` changed between when a token was issued
  and when it's being verified (a config change, or a rolling deploy with mismatched secrets across
  instances), or the token was tampered with. Rotating `JWT_SECRET` invalidates every
  already-issued token instantly — there's no way around that with a symmetric secret.
- **`bcrypt.CompareHashAndPassword` is "slow"** — that's not a bug to fix, it's bcrypt's entire
  design. If login latency genuinely matters at your request volume, that's a signal to look at
  concurrency (are logins actually parallelized?) before considering a weaker hash.

## Do's and don'ts

- **Do** return the same error for "no such user" and "wrong password," always, at every layer —
  it only takes one layer accidentally leaking the distinction (a differently-worded error message,
  a different HTTP status, even a measurably different response time) to make the other layers'
  carefulness pointless.
- **Do** keep `Secret` long and random — generated with a real CSPRNG, not typed by hand. A short
  or guessable HMAC secret defeats the whole scheme regardless of how correct the surrounding code
  is.
- **Don't** put a password anywhere in a log line, an error message, or a returned struct — not
  even hashed. `PasswordHash` exists on `domain.User` for the repository to read and write; nothing
  above the service layer should ever see it.
- **Don't** implement a signup flow, a password-reset flow, or refresh tokens as part of following
  this chapter — they're real, common needs, but each is its own design problem (rate limiting on
  signup, secure token delivery on reset, revocation on refresh) big enough to deserve its own
  treatment rather than being bolted onto this tutorial's two seeded demo accounts.

## Next

[Chapter 10: API layer](10-api-layer.md) — turning all of this into actual HTTP endpoints.
