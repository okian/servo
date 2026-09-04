# 3. Configuration

Every package we're about to write needs to know something from the outside world — a database
address, a secret key, how long a token should live. This chapter builds the mechanism that supplies
those values, and it makes one decision that shapes every chapter after it: **each package declares
the settings it needs, and no package declares settings for anybody else.**

> Servo can now generate this entire chapter's machinery: mark a struct with
> [`//servo:config`](../reference/config.md) and the parsing, the defaults, and the
> missing-variable errors are emitted as plain Go, with no reflection library and no `NewConfig`
> provider to write. The chapter still builds it by hand, deliberately — the *design* it teaches
> (each package owns its settings, namespaced by prefix, filled from a source you can fake) is
> exactly the design the directive generates, and having built it once you'll know precisely what
> the generated version is doing for you.

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
narrow struct is declared *by* that package. Doing both is this chapter.

## What the config package does instead

Create `config/config.go`. It declares no settings at all — it supplies values without knowing what
any of them mean:

```go
package config

// Source is where configuration values come from.
type Source interface {
	Values() map[string]string
}
```

That's the whole contract. A `Source` hands over key/value pairs; deciding which keys matter is
somebody else's job.

The implementation reads the process environment once, at construction:

```go
type Env struct{ values map[string]string }

func NewEnv() *Env {
	environ := os.Environ()
	values := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			values[k] = v
		}
	}
	return &Env{values: values}
}

func (e *Env) Values() map[string]string { return e.values }
```

Reading once matters: nothing constructed later can observe the environment changing underneath it.

Finally, the piece that fills a package's own struct:

```go
func Parse[T any](src Source, prefix string) (*T, error) {
	cfg, err := env.ParseAsWithOptions[T](env.Options{
		Environment: src.Values(),
		Prefix:      prefix,
	})
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}
```

It's generic precisely so the type stays in the package that declares it. `Parse` never names a
field and never needs changing when one is added — that is the property the whole design is for.

We're using [`github.com/caarlos0/env`](https://github.com/caarlos0/env), which reads requirements
and defaults off struct tags. `ParseAsWithOptions` takes the environment as a map rather than
reading the process's own, which is what lets a `Source` be anything.

## What a package that needs configuration looks like

Every package from here on follows this shape. `redis`, from [chapter 6](06-caching-layer.md):

```go
package redis

const envPrefix = "REDIS_"

type Config struct {
	Addr string `env:"ADDR,required"`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, envPrefix)
}

func New(cfg *Config) *Cache {
	return &Cache{client: goredis.NewClient(&goredis.Options{Addr: cfg.Addr})}
}
```

Four lines of declaration, and the settings live next to the code that reads them. Adding a timeout
is one edit, in this file.

### The prefix is what makes the field names free

`redis.Config` calls its field `Addr`, not `RedisAddr`. So does `natsbroker.Config` call its field
`URL`, not `NATSURL`. Neither has to know about the other, because the prefix namespaces them:
`REDIS_ADDR` and `NATS_URL` on the wire, plain `Addr` and `URL` in Go.

The prefix is a `const` beside the fields rather than a literal at the call site, so the
deployment-facing names and the struct that consumes them can't drift apart, and so you can grep for
`NATS_` and find the code that reads it.

Errors still name the full variable, which is the part that matters at 3am:

```
env: required environment variable "REDIS_ADDR" is not set
```

not `"ADDR"`, which would tell an operator nothing.

Settings that are genuinely service-wide pass `""` and keep their bare names — `api.Config` does
this for `HTTP_ADDR` and `ADMIN_ADDR`, and `observability.Config` for `LOG_LEVEL` and
`OTLP_ENDPOINT`.

### Why a Source, rather than each package calling `os.Getenv`

The narrow struct alone doesn't require any of this machinery — `redis` could parse its own
environment directly and still own its fields. The `Source` buys two things.

A package that reads the environment *assumes there is one*. Taking a `Source` as an ordinary
constructor parameter means a test says what it wants rather than arranging the process environment
to produce it, and means the values could come from somewhere else entirely — a file, a secret
store, a flat KV store like Consul — without touching a single component.

And it makes configuration an ordinary node in the graph. `*config.Env` is a dependency like any
other; servo constructs it first because everything else needs it, and nothing needs a registration
step or a global.

## Prove it works

The tests belong to `config`, and they test the mechanism rather than any particular setting —
because the mechanism is all this package has:

```go
// config/config_test.go

// static is a Source with values supplied directly — the reason Source is
// an interface at all.
type static map[string]string

func (s static) Values() map[string]string { return s }

// probe stands in for any package's own config type. This package never
// sees a real one, which is the point: Parse names no field.
type probe struct {
	Addr     string        `env:"ADDR,required"`
	Retries  int           `env:"RETRIES"`
	Timeout  time.Duration `env:"TIMEOUT"`
	Fallback int           `env:"FALLBACK" envDefault:"7"`
}

func TestParseConvertsTypedFieldsFromAStringSource(t *testing.T) {
	cfg, err := Parse[probe](static{
		"P_ADDR": "localhost:1", "P_RETRIES": "3", "P_TIMEOUT": "90s",
	}, "P_")
	...
}
```

`Values()` returns strings, but the struct tags drive the conversion — `int`, `bool`, `float64`,
`time.Duration` and slices all parse from their normal string forms, and `envDefault` applies to
anything the source doesn't carry.

Two more tests pin the properties that would otherwise erode:

```go
// The prefix is what lets two packages each declare a field called Addr
// without coordinating.
func TestPrefixKeepsTwoPackagesApart(t *testing.T) { ... }

// An injected Source must be the whole truth: if the process environment
// leaked in, a test could pass because of the machine it ran on.
func TestAnInjectedSourceDoesNotFallBackToTheProcessEnvironment(t *testing.T) {
	t.Setenv("P_ADDR", "from-the-process")
	if _, err := Parse[probe](static{}, "P_"); err == nil {
		t.Error("Parse saw P_ADDR from the process environment; the Source should be the only input")
	}
}
```

That last one is the guarantee that makes every other package's tests trustworthy.

```
$ go test ./config/...
ok  	example.com/servoorders/internal/config	0.104s
```

## The trade this design makes

Be clear-eyed about what was given up. With one central struct, a single `env.Parse` ran before
anything was constructed, so a fresh deployment missing three variables reported all three at once,
and the process either had a complete configuration or hadn't started.

Now each package parses its own during construction, and servo's generated `New` returns on the
first error. A deployment missing `POSTGRES_DSN`, `REDIS_ADDR` and `JWT_SECRET` reports the first
one it reaches; you fix it, redeploy, and learn about the second.

Three things soften that. The error still names the exact variable. Every config node sits at the
same level in the graph, and servo constructs a level concurrently, so in practice you often *do*
see several at once — though which ones is a scheduling detail, not a promise. And nothing
partially starts: the failure is still during `New`, before `Run`.

What's genuinely lost is the guarantee of *completeness*. "It started" used to mean every declared
setting was present; now it means every setting on the path constructed so far was present. If that
matters more than the coupling does, call every package's `NewConfig` in `main` before `New`,
collect the errors with `errors.Join`, and fail once — about fifteen lines, and `config` stays
ignorant.

## Diagnostics

- **`env: required environment variable "REDIS_ADDR" is not set`** — the package working as
  intended. Set the variable; don't reach for a default just to silence it unless that default is
  genuinely safe in every environment you'll run this in.
- **A `time.Duration` field fails to parse** — the value has to be a Go duration string (`"1h30m"`,
  `"500ms"`), not a bare number. `caarlos0/env` won't guess units.
- **A setting is ignored and the field keeps its zero value** — check the prefix. `REDIS_ADDR` with
  `envPrefix = "REDIS_"` and `env:"ADDR"` is right; `env:"REDIS_ADDR"` with the same prefix looks for
  `REDIS_REDIS_ADDR`.
- **Config looks right locally but wrong in Docker or CI** — almost always a variable set in your
  shell that never reached the container or job. `docker compose`'s `environment:` block and the CI
  workflow's `env:` block are the two places it has to be set; see
  [chapter 18](18-cicd.md) and [chapter 19](19-running-and-deployment.md).

## Do's and don'ts

- **Do** make every secret `,required` with no default. A convenient fallback for a signing key or a
  database password is a vulnerability waiting for someone to forget to override it in production.
- **Do** declare the prefix as a `const` beside the fields it applies to.
- **Do** keep each package's `Config` a flat struct of primitives. If one needs nesting, that is
  usually a sign the package is doing too much.
- **Don't** call `os.Getenv` in a component. Take a `config.Source`, or take the narrow `*Config`
  that a `NewConfig` built from one — a stray `os.Getenv` is invisible to tests and to the graph.
- **Don't** declare a setting in `config`. It has no fields on purpose, and the first one added
  there starts the drift this chapter exists to avoid.
- **Don't** commit a `.env` file with real values, even "just for local dev." Commit `.env.example`
  with placeholders; gitignore `.env` itself.

## Next

[Chapter 4: Domain layer](04-domain-layer.md) — the types every other layer will build around.
