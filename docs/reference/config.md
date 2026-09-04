# Generated configuration

**Who this is for:** anyone marking a struct with `//servo:config`, wiring a config file into an
injector, or debugging why a setting didn't arrive.

Most components need a handful of settings — an address, a pool size, a timeout, a password — and
most codebases answer that with a reflection-based env library and a hand-written `NewConfig`
provider per package. Servo generates that code instead: mark a struct with a directive, tag its
fields, and `servo generate` writes a plain-Go loader for it — defaults, file section, environment,
validation of the *tags themselves* at generate time, and one startup error that names every
variable an operator forgot to set.

Because the loader is generated **into the config's own package**, the struct and every one of its
fields can stay unexported. This is the part no reflection library can do at all: `caarlos0/env`
and friends cannot set an unexported field, and cannot tell you at build time that a tag is
misspelled.

## The directive

```go
//servo:config prefix=POSTGRES
type dbConfig struct {
	dsn          string        `config:"dsn,required"`              // POSTGRES_DSN
	maxConns     int32         `config:"max_conns,default=10"`      // POSTGRES_MAX_CONNS
	connLifetime time.Duration `config:"conn_lifetime,default=30m"` // POSTGRES_CONN_LIFETIME
	password     string        `config:"password,required,secret"`  // POSTGRES_PASSWORD
}

func New(cfg dbConfig) (*Store, error) { ... }
```

`//servo:config` goes in the type's doc comment, exactly like `//go:generate`: line start, no space
after `//`. It takes two options:

- **`prefix=`** (required) — `UPPER_SNAKE`. Every tagged field becomes the environment variable
  `PREFIX_NAME`, and, lowercased, the prefix names the config-file section.
- **`key=`** (optional) — `lower_snake`. Overrides the file-section key when the lowercased prefix
  isn't what you want in the file.

The directive is servo's only comment directive, and it is validated twice: `servo generate`
refuses a malformed one, and [`servo-vet`](cli.md#servo-vet) flags a typo'd one
(`//servo:confg`) in the editor — a misspelled comment directive otherwise compiles, generates,
and silently loads nothing.

Rules that are deliberate, not accidental:

- **One config type per package.** The generated loader has a fixed exported name
  (`ServoConfig`), and a package with two would need derived names. It also matches how configs
  are actually written — one struct of settings beside the component that uses them.
- **Main module only.** The companion file is written into the declaring package's directory, and
  nothing may write into the module cache.
- **A hand-written constructor for a config type is a diagnostic.** The directive resolves ahead
  of provider selection, so the constructor would never run — servo says so instead of letting it
  sit in the code looking authoritative.

## The tag grammar

Four words, deliberately: `config:"name"` plus `required`, `default=<value>`, and `secret`.

| option | meaning |
|---|---|
| *name* | `lower_snake`. Env var is `PREFIX_NAME`; file key is `section.name`. |
| `required` | Missing from every source is a startup error. Mutually exclusive with `default=`. |
| `default=v` | Applied first, before file and env. Validated **at generate time** against the field's type — a bad default is a `servo generate` error, never a 3 a.m. one. |
| `secret` | No generated error path ever echoes the value. A malformed `POSTGRES_PASSWORD` reports "value redacted: secret" where any other field would quote what it saw. |

Field types are a closed set: `string`, `bool`, the sized ints and uints, `float32`/`float64`,
and `time.Duration`. A defined type (`type Port int`) is rejected rather than converted, nested
structs are a "not yet supported" diagnostic, and an untagged field is simply not loaded —
derived state a constructor fills in later is an ordinary thing for a config struct to carry.

Anything smarter than these four words — ranges, cross-field rules — belongs in the constructor
that receives the struct. It already returns an error at exactly the right moment in the
lifecycle, so servo does not grow a validation DSL.

## Precedence: default, then file, then environment

The environment always wins. That order exists so an operator can override any file value in a
deployment without editing the file — and it is per *setting*, not per section, so a file can set
`max_conns` while the environment sets `dsn`.

A required setting is satisfied by either source; missing from both, it lands in one startup
error that names every spelling that would fix it:

```
postgres: missing required configuration: POSTGRES_DSN (env) or postgres.dsn (config file)
```

## The ConfigFile marker

```go
servo.Build(
	servo.Root[*api.Server](),
	servo.ConfigFile("config.yaml"),
)
```

Without it, loaders read only the environment and take no arguments. With it, the injector reads
the declared file once at the top of `New`, and every loader receives the decoded map.

The path must be a **string literal** ending in `.json`, `.yaml`, `.yml`, or `.toml` — it is read
as syntax, and the extension decides which decoder the generated code carries. Dependency hygiene
falls out of that: an env-only or JSON app is stdlib-only, a yaml app gains `gopkg.in/yaml.v3` and
nothing else, a toml app gains `github.com/BurntSushi/toml`. The decoders are imported by the
*generated file in your module* — servo's own module depends on none of them.

At runtime:

- **`CONFIG_FILE`** overrides the path (same extension family only — a yaml binary refuses a
  `.json` path with "unsupported extension" rather than misparsing it).
- The **declared** path being absent is not an error: every setting can still arrive from the
  environment, which is exactly how prod deployments that are env-only anyway behave.
- A path set **explicitly** via `CONFIG_FILE` must exist — the operator asked for it.

The file's sections are the config types' section keys:

```yaml
postgres:
  dsn: postgres://localhost/orders
  max_conns: 40
```

One `config:` tag drives every source. There are no `json:`/`yaml:`/`toml:` tags to keep in sync,
because servo generates the map-walking code per format instead of asking three reflection
libraries to agree — and the number normalization those decoders disagree on (JSON says `float64`,
yaml.v3 says `int`, TOML says `int64`) lives once, tested, in the tiny stdlib-only
`github.com/okian/servo/v3/conf` package.

**Agreement rule:** a config's companion loader is one file with one signature, so every injector
in the module that uses the config must agree — all of them declare a `ConfigFile`, or none.
`servo generate` refuses the mix before writing anything.

## What gets generated

Beside the config type, `servo_config_gen.go` (gated `!servoinject`, like every generated file):

```go
func ServoConfig() (dbConfig, error)                    // env-only
func ServoConfig(file map[string]any) (dbConfig, error) // with a ConfigFile
```

Its header comment is the settings table; its body is steppable strconv/`time.ParseDuration`
parsing in precedence order. `ServoConfig` is the one exported identifier the package gains — the
export exists because the injector lives in another package and Go has no narrower door, and it
returns the unexported type, which the injector receives by `:=` inference and never names.

In the injector, the config is loaded at the top of `New`, before anything is constructed, and
held as a **local, not an `App` field** — a field of an unexported foreign type would not compile.
It appears at level 0 in the graph header, `App.Graph()`, `servo graph`, `explain` and `why`.
`servo check` diffs companion files exactly as it diffs `servo_gen.go`.

Two consequences of "local, not field":

- **A scoped constructor cannot depend on a config type** — scoped constructions read borrowed
  singletons off the `App`. The diagnostic names the workaround: wrap the values in a singleton of
  your own (`func NewSettings(cfg dbConfig) *Settings`) and let the scoped component take that.
- **`servo.Value[T]` beats the directive**, like it beats every provider: declaring one says "the
  caller supplies this", and the loader is then not consulted and not generated for that graph.

## Testing

Unit tests of the component construct the struct literally — they are in the config's own package,
where the unexported fields are visible — and call `New` directly. App-level tests set real
environment variables with `t.Setenv`, which exercises the same generated parsing production runs
instead of bypassing it. `NewTestApp` (the [`Override`](spec.md#override) variant) loads configs
identically.

## The `servo config` command

The generator knows every setting the binary reads, so it can print the operator's manual —
something no runtime-reflection library can do without executing the binary:

```
$ servo config
configuration for example.com/orders/cmd/orders
file: config.yaml (override the path with CONFIG_FILE; environment always wins)

  ENV                     FILE KEY                TYPE             MISSING?      FIELD
  POSTGRES_DSN            postgres.dsn            string           required      dbConfig.dsn  postgres/postgres.go:14
  POSTGRES_MAX_CONNS      postgres.max_conns      int32            default 10    dbConfig.maxConns  postgres/postgres.go:15
  POSTGRES_PASSWORD       postgres.password       string (secret)  required      dbConfig.password  postgres/postgres.go:17
```

`--json` emits the same rows as objects. See [CLI commands](cli.md#config).

## Diagnostics

| message contains | cause | fix |
|---|---|---|
| `unrecognized servo directive` | `//servo:` followed by anything but `config` | fix the spelling |
| `needs prefix=UPPER_SNAKE` / `must be UPPER_SNAKE` | missing or malformed prefix option | give the directive a prefix |
| `second //servo:config type in package` | two directives in one package | merge the structs or split the package |
| `unsupported type` | a field type outside the closed set | use a supported type, or load a string and convert in the constructor |
| `both required and has a default` | contradictory tag options | pick one |
| `default %q is not a valid` | default text doesn't parse as the field's type | fix the default |
| `constructs %s, which carries a //servo:config directive` | a hand-written provider for a config type | delete the constructor or drop the directive |
| `resolve to the same environment variable` / `same config file key` | two used configs claim one name | change a tag name or a prefix |
| `scoped constructor depends on config type` | a scope member takes a config | wrap the values in a singleton of your own |
| `servo.ConfigFile(...) is declared, but no //servo:config type is in this graph` | a file nothing would read | mark a struct, or delete the declaration |
| `declare a ConfigFile in both injectors or neither` | two injectors share a config and disagree | make them agree |
