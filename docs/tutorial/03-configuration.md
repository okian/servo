# 3. Configuration

Every package we're about to write needs to know something from the outside world — a database
address, a secret key, how long a token should live. Rather than let each one read the environment
for itself, we'll build one small package that reads it once, validates it, and hands out a typed
struct everyone else depends on. This is the first package in the service, and it's worth getting
right before anything depends on it.

## Why not just `os.Getenv` everywhere

It's tempting to skip straight to `os.Getenv("POSTGRES_DSN")` wherever a value is needed. Two
problems only show up once the service is actually running: a typo in the variable name returns an
empty string instead of failing, so a missing DSN doesn't surface until something tries to use it
three layers down — and there's no single place to see everything the service needs configured.
We'll use [`github.com/caarlos0/env`](https://github.com/caarlos0/env) instead, which reads
requirements and defaults off struct tags.

## Build the config struct

Create `config/config.go`. Start with the struct itself:

```go
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8080"`

	PostgresDSN string `env:"POSTGRES_DSN,required"`
	RedisAddr   string `env:"REDIS_ADDR,required"`
	NATSURL     string `env:"NATS_URL,required"`

	JWTSecret string        `env:"JWT_SECRET,required"`
	JWTExpiry time.Duration `env:"JWT_EXPIRY" envDefault:"1h"`

	LogLevel     string `env:"LOG_LEVEL" envDefault:"info"`
	OTLPEndpoint string `env:"OTLP_ENDPOINT" envDefault:""`

	RateLimitRPS float64 `env:"RATE_LIMIT_RPS" envDefault:"50"`
}
```

A few fields here point at chapters that don't exist yet (`JWTSecret` in chapter 9,
`OTLPEndpoint` in chapter 12) — that's intentional. Deciding the full shape of configuration once,
now, means nothing later has to come back and retrofit a field in here; it just starts using one
that was already waiting.

Notice `JWTSecret` has no `envDefault`. That's not an oversight — we'll come back to exactly why
in the do's and don'ts below, but the short version: a secret that falls back to something
convenient when you forget to set it is a vulnerability with a delay timer on it.

Now add the constructor:

```go
func New() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}
```

`,required` on a field means `New` returns an error the moment that variable is missing — at
startup, before anything downstream tries to use a zero-value DSN and fails in a way that's harder
to trace back to "oh, a config value was never set." `time.Duration` and `float64` fields parse
from their normal string forms (`"1h"`, `"50"`) with no extra code from us.

## Why this shape matters for what comes next

`New() (*Config, error)` looks like an ordinary constructor because it is one — this is exactly
the `(T, error)` shape servo's scanner already knows how to recognize (see servo's own
[README](../../README.md#interfaces-vs-concrete-types)). Every component we write from here on
that needs configuration will just take `*Config` as a constructor parameter, the same way it
would take any other dependency — no registration step, no global variable to reach into. We won't
actually wire anything with servo until [chapter 11](11-wiring-with-servo.md), but the constructor
shape we choose in every chapter between now and then is what makes that chapter possible; `Config`
is the first one to get it right.

## Prove it works

Before moving on, write the two tests that pin this down: a missing required variable fails, and
defaults apply correctly.

```go
// config/config_test.go
func TestNewFailsWhenARequiredVarIsMissing(t *testing.T) {
	unsetForTest(t, "POSTGRES_DSN")
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("NATS_URL", "nats://localhost:4222")
	t.Setenv("JWT_SECRET", "test-secret")

	if _, err := New(); err == nil {
		t.Fatal("expected an error when POSTGRES_DSN is unset")
	}
}
```

`unsetForTest` is a small helper worth writing carefully: `t.Setenv(k, "")` looks like it unsets a
variable, but it doesn't — it sets it to an empty string, and `caarlos0/env`'s `,required` check
only asks whether the variable was set at all, not whether it's non-empty. An empty string still
counts as present. The helper does the real thing instead:

```go
func unsetForTest(t *testing.T, key string) {
	t.Helper()
	orig, wasSet := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if wasSet {
			os.Setenv(key, orig)
		}
	})
}
```

Now the second half of the promise from the top of this section — that defaults apply and typed
fields parse correctly:

```go
func TestNewAppliesDefaultsAndParsesTypedFields(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://localhost/orders")
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("NATS_URL", "nats://localhost:4222")
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("JWT_EXPIRY", "30m")

	cfg, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want default \":8080\"", cfg.HTTPAddr)
	}
	if cfg.JWTExpiry != 30*time.Minute {
		t.Errorf("JWTExpiry = %v, want 30m parsed from env", cfg.JWTExpiry)
	}
	if cfg.RateLimitRPS != 50 {
		t.Errorf("RateLimitRPS = %v, want default 50", cfg.RateLimitRPS)
	}
}
```

This one needs no special helper — every variable it cares about is genuinely meant to be set, so
plain `t.Setenv` (which Go's testing package restores automatically after the test) is all it
takes. Setting `JWT_EXPIRY` explicitly and checking it parsed to `30*time.Minute` is what actually
proves the typed-field parsing works, not just the plain-string fields.

Run both:

```
$ go test ./config/... -v
=== RUN   TestNewFailsWhenARequiredVarIsMissing
--- PASS: TestNewFailsWhenARequiredVarIsMissing (0.00s)
=== RUN   TestNewAppliesDefaultsAndParsesTypedFields
--- PASS: TestNewAppliesDefaultsAndParsesTypedFields (0.00s)
PASS
ok  	example.com/servoorders/config	0.104s
```

## Diagnostics

- **`config: required environment variable "POSTGRES_DSN" is not set`** — this is the package
  working as intended. Set the variable; don't reach for a default just to silence the error unless
  that default is genuinely safe in every environment you'll ever run this in.
- **A `time.Duration` field fails to parse** — the value has to be a Go duration string
  (`"1h30m"`, `"500ms"`), not a bare number. `caarlos0/env` won't guess units for you.
- **Config looks right locally but wrong in Docker or CI** — almost always a variable that's set in
  your shell but never reached the container or job. `docker compose`'s `environment:` block and
  the CI workflow's `env:` block are the two places this actually has to be set — we'll hit both in
  [chapter 15](15-cicd.md) and [chapter 16](16-running-and-deployment.md).

## Do's and don'ts

- **Do** make every secret `,required` with no default. A convenient fallback for `JWTSecret` or a
  database password is a vulnerability waiting for someone to forget to override it in production.
- **Do** keep `Config` a flat struct of primitives. The moment it needs nested structs or
  nested-`env` prefixes, that's usually a sign the service is doing too much in one binary.
- **Don't** read configuration anywhere except `config.New()`. If a package needs a setting, pass
  it in as a constructor parameter — never a second, ad hoc `os.Getenv` call somewhere else, which
  quietly defeats the whole point of having one typed, validated source of truth.
- **Don't** commit a `.env` file with real values, even "just for local dev." Commit
  `.env.example` with placeholders; gitignore `.env` itself.

## Next

[Chapter 4: Domain layer](04-domain-layer.md) — the types every other layer will build around.
