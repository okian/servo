# 3. Configuration

Every package we're about to write needs to know something from the outside world — a database
address, a secret key, how long a token should live. This chapter sets up how those values arrive,
and it makes one decision that shapes every chapter after it: **each package declares the settings
it needs, and no package declares settings for anybody else.**

That's worth being deliberate about, because the obvious design is the other one.

## The obvious design, and what's wrong with it

The natural first move is one struct holding everything:

```go
package config

type Config struct {
	HTTPAddr    string        `env:"HTTP_ADDR" envDefault:":8080"`
	PostgresDSN string        `env:"POSTGRES_DSN,required"`
	RedisAddr   string        `env:"REDIS_ADDR,required"`
	JWTSecret   string        `env:"JWT_SECRET,required"`
	JWTExpiry   time.Duration `env:"JWT_EXPIRY" envDefault:"1h"`
	// ...and one field for every other setting in the service
}
```

with every package taking `*config.Config`. It reads well, it's one place to look, and plenty of
real services do exactly this. Two things go wrong as it grows.

**Adding a setting means editing two packages.** A new Redis timeout belongs to `redis`, but the
field has to be declared in `config`, which has no interest in it. The package that owns the
behaviour and the package that owns the declaration drift apart, and `config` slowly accumulates
knowledge of everything in the service.

**Every component can read every setting.** `natsbroker` takes `*config.Config` because it needs
`NATSURL`, and gets the JWT secret in the same argument. Nothing stops it using that, and nothing in
its signature says it doesn't.

The fix for the second problem is a narrow struct per package. The fix for the first is that the
narrow struct is declared *by* that package. Doing both used to mean building a small parsing
mechanism yourself — a source abstraction, a reflection-based env library, and a `NewConfig`
provider in every package that has settings. Servo generates all of it instead.

## The directive

A package that needs configuration declares a struct, marks it, and takes it as a constructor
parameter. `redis`, from [chapter 6](06-caching-layer.md), in full:

```go
package redis

//servo:config prefix=REDIS
type Config struct {
	Addr string `config:"addr,required"`
}

func New(cfg Config) *Cache {
	return &Cache{client: goredis.NewClient(&goredis.Options{Addr: cfg.Addr})}
}
```

That's the whole package-side story: no provider to write, no parsing library to import, no
`config` package to depend on. When `servo generate` runs it notices the directive, notices that
`New` asks for the type, and writes `servo_config_gen.go` beside it — a loader that reads
`REDIS_ADDR`, applies defaults, and reports what's missing. The injector calls it before
constructing anything, so `New` receives a filled, validated struct the same way it would receive
any other dependency.

The tag grammar is deliberately four words: the name, `required`, `default=<value>`, and `secret`.

```go
//servo:config prefix=JWT
type Config struct {
	Secret string        `config:"secret,required,secret"`
	Expiry time.Duration `config:"expiry,default=1h"`
}
```

Anything smarter — ranges, cross-field rules — belongs in the constructor that receives the
struct, which already returns an error at exactly the right moment in the lifecycle. Servo does
not grow a validation language.

### The prefix is what makes the field names free

`redis.Config` calls its setting `addr`, not `redis_addr`. So does `natsbroker.Config` call its
setting `url`, not `nats_url`. Neither has to know about the other, because the prefix namespaces
them: `REDIS_ADDR` and `NATS_URL` on the wire, plain `Addr` and `URL` in Go. The prefix sits in
the directive, directly above the fields it applies to, so the deployment-facing names and the
struct that consumes them cannot drift apart — and `grep NATS_` still finds the code that reads it.

The prefix is *required*. The hand-rolled version of this design kept an escape hatch — pass `""`
and claim a bare, "app-wide" name like `LOG_LEVEL` — and the directive deliberately doesn't offer
it, because an unowned name is exactly how two packages end up quietly reading one variable. If two
configs in one graph do resolve a setting to the same variable, `servo generate` refuses with both
declaration sites named, instead of letting both fields silently read one value. This service's
observability package pays the toll: its settings are `OBS_LOG_LEVEL` and `OBS_OTLP_ENDPOINT`, and
in exchange every variable in the deployment has exactly one owner you can grep to.

Errors still name the full variable, which is the part that matters at 3am — and a config with
several missing settings reports all of them at once, not the first:

```
redis: missing required configuration: REDIS_ADDR
```

### What actually gets generated

`servo_config_gen.go` is ordinary, steppable Go — the same promise as `servo_gen.go` itself. For
the JWT config above, the heart of it:

```go
func ServoConfig() (Config, error) {
	var cfg Config
	var missing []string

	// secret — JWT_SECRET (required)
	if v, ok := os.LookupEnv("JWT_SECRET"); ok {
		cfg.Secret = v
	} else {
		missing = append(missing, "JWT_SECRET")
	}

	// expiry — JWT_EXPIRY
	cfg.Expiry = time.Hour
	if v, ok := os.LookupEnv("JWT_EXPIRY"); ok {
		val, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("JWT_EXPIRY: not a valid time.Duration: %q", v)
		}
		cfg.Expiry = val
	}
	...
}
```

Three properties are worth noticing, because no runtime reflection library can offer them:

- **The tags are validated when you generate, not when you deploy.** A misspelled option, an
  unsupported field type, or a `default=1x` that doesn't parse as a duration fails
  `servo generate` with a position — never a 3am page.
- **The struct can be entirely unexported.** The loader lives in the same package, so a config
  type and every one of its fields can be private to the package that owns them. This service
  keeps its `Config` types exported because tests in other packages construct them, but nothing
  requires that.
- **`secret` means what it says.** The parse-failure path for `JWT_SECRET` reports
  `(value redacted: secret)` where any other field would quote the value it rejected, so a
  malformed secret can't leak into a log line.

Commit the generated files, like `servo_gen.go` — `servo check` diffs them in CI and refuses
drift. And a typo'd directive (`//servo:confg`) is just a comment to the compiler, which is why
`servo-vet` flags it in your editor before generation silently loads nothing.

## Configuration is an ordinary part of the graph

A config type is a node like any other: it sits at level 0, everything that takes it sits above
it, and `servo explain redis.Config` answers for it the way it answers for anything else. Which
means the generator can also do something genuinely new — print the operator's manual for a
binary without running it:

```
$ servo config --dir cmd/orders
configuration for example.com/servoorders/cmd/orders

  ENV               TYPE            MISSING?      FIELD
  HTTP_ADDR         string          default :8080 Config.HTTPAddr
  HTTP_ADMIN_ADDR   string          default :8081 Config.AdminAddr
  JWT_EXPIRY        time.Duration   default 1h    Config.Expiry
  JWT_SECRET        string (secret) required      Config.Secret
  NATS_URL          string          required      Config.URL
  OBS_LOG_LEVEL     string          default info  Config.LogLevel
  OBS_OTLP_ENDPOINT string          zero value    Config.OTLPEndpoint
  POSTGRES_DSN      string          required      Config.DSN
  RATE_LIMIT_RPS    float64         default 50    Config.RPS
  REDIS_ADDR        string          required      Config.Addr
  SESSION_RECENT    int             default 10    Config.Recent
```

That table is derived from the resolved graph, so it's complete and current by construction —
a config type nothing depends on doesn't appear, exactly as it doesn't reach the binary. This is
the whole deployment contract of `cmd/orders`, and chapters [18](18-cicd.md) and
[19](19-running-and-deployment.md) set exactly these variables.

One setting has an owner you might not expect: `NATS_URL` belongs to `natsbroker` alone, even
though the notifier — the consuming end of the messaging layer — connects to the same server.
The notifier takes `natsbroker.Config` as a parameter rather than declaring its own copy, because
two structs tagged to read one variable is the collision the generator refuses. One setting, one
owner; [chapter 7](07-messaging-layer.md) shows the shape.

## Prove it works

There is no parsing mechanism left in this module to test — the loader is generated, and servo's
own test suite is what pins its behaviour. What's left to prove is *this service's* configuration,
and that happens at two levels.

Unit tests construct the struct literally and call the constructor, exactly as they would with any
other parameter — no environment involved, nothing to fake:

```go
issuer := auth.New(auth.Config{Secret: "test-secret", Expiry: time.Hour})
```

App-level tests — the `NewTestApp` tests of [chapter 13](13-wiring-with-servo.md) — set real
variables with `t.Setenv`, which exercises the same generated parsing production runs instead of
bypassing it:

```go
t.Setenv("JWT_SECRET", "test-secret")
t.Setenv("NATS_URL", "unused-in-this-test")
```

`t.Setenv` handles cleanup and refuses to run in a parallel test, so the standard library already
owns the seam a hand-rolled `Source` abstraction used to provide.

## The trade this design makes

Be clear-eyed about what per-package configs give up against one central struct. There, a single
parse ran before anything was constructed, so a deployment missing three variables across three
packages reported all three at once. Here, each config loads separately at the top of `New`, and
the first one that fails is the one you hear about; you fix `POSTGRES_DSN`, redeploy, and learn
about `REDIS_ADDR`.

Three things soften that. *Within* one config, everything missing is reported together — the
loader collects its required settings into one error rather than stopping at the first. The error
always names the exact variable. And nothing partially starts: the failure is still inside `New`,
before `Run`, so "it started" still means "everything on the constructed path was present."
`servo config` is the completeness check the central struct used to be: one command lists every
variable the binary reads, which is more than the big struct ever told you.

## Also in the box: a config file

This service is configured entirely by environment variables — it runs in containers, and env is
what containers do. If you want a `config.yaml`/`.json`/`.toml` alongside, one marker in the spec
turns it on:

```go
servo.Build(
	servo.Root[*api.Server](),
	servo.ConfigFile("config.yaml"),
)
```

Every `//servo:config` type then also reads its section of the file (`redis.addr` under `redis:`),
with precedence default → file → environment — the environment always wins, so an operator can
override any file value in a deployment without editing it. The same `config:` tags drive both
sources; there are no `yaml:` tags to keep in sync. The full contract, including which decoder the
generated code carries and why an env-only app never gains a yaml dependency, is in
[the reference](../reference/config.md).

## Diagnostics

- **`redis: missing required configuration: REDIS_ADDR`** — the loader working as intended. Set
  the variable; don't reach for a default just to silence it unless that default is genuinely safe
  in every environment you'll run this in.
- **`JWT_EXPIRY: not a valid time.Duration: "90"`** — durations are Go duration strings
  (`"1h30m"`, `"500ms"`), not bare numbers.
- **`//servo:config needs prefix=UPPER_SNAKE`**, **`unknown option`**, **`default %q is not a
  valid ...`** — generate-time refusals of a malformed directive or tag. The position points at
  the declaration; fix it there. `servo-vet` shows the same squiggle in your editor.
- **`two config fields resolve to the same environment variable`** — two used configs claim one
  name. Change a tag name or a prefix; the diagnostic names both declaration sites.
- **`X constructs Y, which carries a //servo:config directive`** — a leftover hand-written
  `NewConfig`-style provider. The generated loader always wins selection, so servo refuses to let
  the constructor sit in the code looking authoritative; delete it.
- **A setting is ignored and the field keeps its zero value** — check the spelling against
  `servo config`'s table. The env name is always `PREFIX_NAME` with the tag's name uppercased:
  `config:"addr"` under `prefix=REDIS` reads `REDIS_ADDR`, never `ADDR` alone.
- **Config looks right locally but wrong in Docker or CI** — almost always a variable set in your
  shell that never reached the container or job. `docker compose`'s `environment:` block and the CI
  workflow's `env:` block are the two places it has to be set; see
  [chapter 18](18-cicd.md) and [chapter 19](19-running-and-deployment.md).

## Do's and don'ts

- **Do** make every secret `required` with no default, and tag it `secret`. A convenient fallback
  for a signing key is a vulnerability waiting for someone to forget to override it in production —
  and the `secret` tag is what keeps a malformed value out of your logs.
- **Do** run `servo config` when writing deployment manifests; it is the table chapters 18 and 19
  were written from.
- **Do** keep each package's `Config` a flat struct of primitives. Nesting isn't supported, and
  wanting it is usually a sign the package is doing too much.
- **Don't** call `os.Getenv` in a component. Declare the setting on the package's config struct —
  a stray `os.Getenv` is invisible to `servo config`, to tests, and to the graph.
- **Don't** write a constructor for a config type. The directive already provides one; servo will
  refuse the ambiguity rather than pick silently.
- **Don't** commit a `.env` file with real values, even "just for local dev." Commit `.env.example`
  with placeholders; gitignore `.env` itself.

## Next

[Chapter 4: Domain layer](04-domain-layer.md) — the types every other layer will build around.
