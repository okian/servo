# 3. Configuration

Every other package in this service needs to know *something* from the environment — a database
DSN, a Redis address, a secret key. The `config` package is where all of that lives, as one typed
struct, loaded once.

## Why a typed struct instead of `os.Getenv` everywhere

Scattering `os.Getenv("POSTGRES_DSN")` across the codebase has two problems that only show up once
the service is already running: a typo in the variable name silently returns an empty string
instead of failing, and there's no single place to see everything the service needs configured.
[`github.com/caarlos0/env`](https://github.com/caarlos0/env) reads struct tags instead:

```go
// examples/tutorial/config/config.go
type Config struct {
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8080"`

	PostgresDSN string `env:"POSTGRES_DSN,required"`
	RedisAddr   string `env:"REDIS_ADDR,required"`
	NATSURL     string `env:"NATS_URL,required"`

	// JWTSecret has no default on purpose — see Do's and don'ts below.
	JWTSecret string        `env:"JWT_SECRET,required"`
	JWTExpiry time.Duration `env:"JWT_EXPIRY" envDefault:"1h"`

	LogLevel     string `env:"LOG_LEVEL" envDefault:"info"`
	OTLPEndpoint string `env:"OTLP_ENDPOINT" envDefault:""`

	RateLimitRPS float64 `env:"RATE_LIMIT_RPS" envDefault:"50"`
}

func New() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}
```

`,required` means `New` returns an error the moment a required variable is missing — at startup,
before anything tries to use a zero-value DSN and fails confusingly three layers deep. `time.Duration`
and `float64` fields parse from their normal string forms (`"1h"`, `"50"`) automatically; nothing
here hand-parses a string.

## Where this fits with servo

`config.New() (*Config, error)` is an ordinary constructor — the `(T, error)` shape servo's scanner
already recognizes (see [servo's own README](../../README.md#interfaces-vs-concrete-types)). Every
component later in this tutorial that needs `*Config` just takes it as a constructor parameter;
there's no separate "register the config" step, and no global. [Chapter
11](11-wiring-with-servo.md) is where this actually gets wired into a graph — for now, `Config`
just needs to exist and be correct, verified by its own tests.

## Try it yourself

```
$ cd examples/tutorial
$ go test ./config/... -v
=== RUN   TestNewFailsWhenARequiredVarIsMissing
--- PASS: TestNewFailsWhenARequiredVarIsMissing (0.00s)
=== RUN   TestNewAppliesDefaultsAndParsesTypedFields
--- PASS: TestNewAppliesDefaultsAndParsesTypedFields (0.00s)
PASS
ok  	example.com/servoorders/config	0.104s
```

The first test proves a missing required variable is a startup error, not a silently-empty string;
the second proves defaults apply and typed fields (`JWTExpiry`, `RateLimitRPS`) parse correctly.
Both set every environment variable they touch with `t.Setenv`, which Go's testing package
automatically restores after the test — no leftover state to clean up by hand, and no risk of one
test's environment leaking into another's.

## Diagnostics

- **`config: required environment variable "POSTGRES_DSN" is not set`** — exactly the failure mode
  this package is designed to produce. Set the variable; don't add a default to make the error go
  away, unless the default is genuinely safe in every environment (see below).
- **A `time.Duration` field fails to parse** — the value must be a Go duration string (`"1h30m"`,
  `"500ms"`), not a bare number. `caarlos0/env` doesn't guess units.
- **Config values are correct locally but wrong in Docker/CI** — almost always an env var set in
  your shell but not exported to the container/job. `docker compose`'s `environment:` block and the
  CI workflow's `env:` block are the two places this actually has to be set (see [chapter
  16](16-running-and-deployment.md) and [chapter 15](15-cicd.md)).

## Do's and don'ts

- **Do** make secrets (`JWTSecret`, a database password) `,required` with no default. A convenient
  fallback for a secret is a vulnerability waiting for someone to forget to override it in
  production.
- **Do** keep `Config` a flat struct of primitives. The moment it needs nested structs or
  nested-`env` prefixes, it's usually a sign the service is doing too much in one binary.
- **Don't** read configuration anywhere except `config.New()`. If a package needs a setting,
  it should arrive as a constructor parameter (directly, or via `*config.Config`) — never a second,
  ad hoc `os.Getenv` call somewhere else in the codebase, which defeats the entire point of having
  one typed, validated source of truth.
- **Don't** commit a `.env` file with real secrets to version control, even a "just for local dev"
  one. `.env.example` with placeholder values, committed; `.env`, gitignored.

## Next

[Chapter 4: Domain layer](04-domain-layer.md) — the types every other layer will build around.
