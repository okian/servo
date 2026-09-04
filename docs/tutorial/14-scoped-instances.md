# 14. Scoped instances

Every component in the last chapter's graph is built once, in `New`, and held until the process
exits. That is the right answer for a connection pool, a JWT issuer, a metrics registry — anything
whose identity doesn't depend on who is asking.

It is the wrong answer for anything that does. This chapter adds one such thing: a **session**, one
per logged-in user, holding the orders that user has looked at recently. Two requests from Alice
share hers; Bob's is a different object; nobody's outlives their being gone for five minutes.

The interesting part isn't the feature. It's that writing this by hand — a map, a mutex, a
reference count, an eviction timer, and a teardown — is somewhere between fifty and a hundred lines
of concurrency you'd have to get exactly right, and one specific way of getting it wrong is
invisible until production. servo generates all of it, and refuses to compile the wrong version.

## The mistake this prevents

Suppose you skip all of it and just inject a `*session.Session` into `api.Server`:

```go
func New(..., sess *session.Session) *Server   // don't
```

That compiles. It runs. Every test passes. And it is a cross-user data leak: `api.Server` is
constructed once, so it captures one session — whichever user's happened to be built first — and
hands that same one to every request forever. Alice sees Bob's recently-viewed orders. Nothing in
the running program says so.

Hold that thought; we'll come back to it once there's a scope to widen.

## The key type

A scope is identified by a **key type**, and it has to be a defined type of your own:

```go
// session/session.go
type UserID string
```

Not `string`. Scope identity is type identity, and if two unrelated scopes both keyed on `string`,
nothing in the generator could tell them apart.

The key gets into a request the same way the claims already do — in the auth middleware, at the one
point where the user's identity is first known:

```go
// session/session.go
type ctxKey struct{}

func WithUser(ctx context.Context, id UserID) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}
```

```go
// transport/api/middleware.go, inside requireAuth
ctx := context.WithValue(r.Context(), claimsKey, claims)
ctx = session.WithUser(ctx, session.UserID(claims.UserID.String()))
next(w, r.WithContext(ctx))
```

Two lines of transport code. servo ships no HTTP adapter on purpose — the moment it does, it stops
being a codegen tool and starts being a framework.

## The scoped type

```go
// session/session.go
type Session struct {
	id       UserID
	settings *Settings

	mu     sync.Mutex
	recent []uuid.UUID
	views  int
}

func New(id UserID, settings *Settings, log *observability.Logger) *Session {
	return &Session{id: id, settings: settings, log: log}
}
```

An ordinary constructor. It takes the key like any other dependency, and `*Settings` like any
other singleton — and that difference is the whole of what servo needs to know. `UserID` varies per
user, so `*Session` is one per user. `*Settings` doesn't, so it stays one shared instance and
is *not* rebuilt fifty thousand times.

Nothing here is annotated. servo works it out from the dependency edges.

`Settings` is worth a second look, because it's the one place a scope touches
[chapter 3](03-configuration.md)'s configuration machinery and finds a rule:

```go
//servo:config prefix=SESSION
type Config struct {
	Recent int `config:"recent,default=10"`
}

// Settings is the singleton carrier between the config and the scope.
type Settings struct {
	Recent int
}

func NewSettings(cfg Config) *Settings {
	return &Settings{Recent: cfg.Recent}
}
```

A `//servo:config` value is loaded at the top of the generated `New` and held as a local there —
never a field on the `App` — while a scope's per-key constructions read every borrowed singleton
*off* the `App`. So a scoped constructor taking `Config` directly is something `servo generate`
refuses, and its diagnostic names exactly this workaround: a singleton of your own that takes the
config once and is shared by every session. Which is also the truthful shape — `SESSION_RECENT`
does not vary with the user, and the type system now says so.

## The extractor

One method turns a context into a key:

```go
func (*Session) ScopeKey(ctx context.Context) (UserID, error) {
	id, ok := ctx.Value(ctxKey{}).(UserID)
	if !ok || id == "" {
		return "", servo.ErrNoScopeKey
	}
	return id, nil
}
```

Two things about this signature are load-bearing, and both have a diagnostic behind them.

**The receiver is unnamed.** Generated code calls this on a typed nil — it has to, because the key
must be known *before* an instance can be chosen, so there is no instance to call it on. A receiver
the body could reach would be a nil dereference in production, and no signature can say "never
touches its receiver". So servo checks: `servo generate` rejects a named receiver, and `servo-vet`
reports it in your editor. A blank `_` receiver is accepted too, but staticcheck's ST1006 flags it
and asks for exactly the form above.

**It returns an error.** Drop it, and a request with no key gets the zero `UserID` — and every
unauthenticated caller silently shares one session. That is the same cross-user leak as before,
arriving through a different door. `servo.ErrNoScopeKey` is the conventional error; any error will
do. (This is the one place a component may import `servo`, and even here it's optional.)

## The accessor interface

servo can't emit a type into your package, so it can't give `api.Server` something to depend on.
You declare that yourself:

```go
// session/session.go
type Sessions interface {
	Acquire(ctx context.Context) (*Session, func(), error)
	Stats() servo.ScopeStats
}
```

Those are the only two methods the generated accessor has. Declare either, both, or — if you add a
third — get a diagnostic at generate time rather than a type error inside a file you were told not
to read.

## The declaration

One marker, alongside the roots:

```go
// cmd/orders/spec.go
servo.Build(
	servo.Root[*api.Server](),
	servo.Root[*notifier.Notifier](),

	servo.Scoped[*session.Session, session.Sessions](
		servo.Linger(5*time.Minute),
		servo.Max(50_000),
	),

	// ... binds and overrides unchanged
)
```

**`Linger`** is how long a session survives its last holder. Without it, the reference count of a
short handler goes 0→1→0 per request and the session is rebuilt every time — which would make the
recently-viewed list permanently empty, since nothing ever survives to the next request. Five
minutes means a user browsing around keeps one session; a user who closes the tab loses it shortly
after.

**`Max`** caps the key space. Keys come from user input, so an uncapped scope is an allocation
primitive anyone can point at your heap. Past the cap, `Acquire` returns `servo.ErrScopeFull`
instead of allocating.

Both arguments must be constants — the spec file is read, never executed. Anything about a scope
that *should* be configurable therefore lives where every other setting does. Add one field to
this package's own `Config` ([chapter 3](03-configuration.md)), carried to the sessions by
`Settings`:

```go
// session/session.go
// recent caps the per-user recently-viewed list. The linger
// window and instance cap for that scope are *not* here: both are
// baked into the generated code from servo.Scoped's arguments, which
// the spec file declares as constants.
Recent int `config:"recent,default=10"`   // SESSION_RECENT, via the directive's prefix
```

## Consuming it

`api.Server` takes the interface, never the instance:

```go
type Server struct {
	// ...
	sessions session.Sessions
}

func New(..., sessions session.Sessions) *Server
```

and acquires per call:

```go
func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) {
	sess, release, err := s.sessions.Acquire(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session unavailable")
		return
	}
	defer release()

	writeJSON(w, http.StatusOK, recentResponse{Recent: sess.Recent()})
}
```

with one more DTO beside the others in `transport/api/dto.go`:

```go
type recentResponse struct {
	Recent []uuid.UUID `json:"recent"`
}
```

and one more route, in `api.New`'s mux, behind the same `requireAuth` as everything else — which
is what puts the key in the context in the first place:

```go
mux.HandleFunc("GET /me/recent", requireAuth(issuer, s.handleRecent))
```

`Acquire`, `defer release()`, use it. That `defer` is the reference unit — not the context.
Cancellation is not completion: a client disconnecting mid-handler cancels the context while your
`defer`s are still running, and freeing the session there would pull it out from under them.
(Forgetting the `release()` isn't fatal — a `context.AfterFunc` backstop releases when the request
ends. Later than ideal, but not never. Which is also why `Acquire` refuses a
`context.Background()`: with no `Done` channel that backstop can never fire.)

## The diagnostic, on purpose

Now go back and make the mistake from the top of the chapter. Change `api.Server`'s field and
`api.New`'s parameter from `session.Sessions` to `*session.Session`, drop the two `Acquire` calls
in `handlers.go` for direct `s.sessions.RecordView(...)` / `s.sessions.Recent()`, and run
`servo generate`. It all compiles; servo refuses anyway:

```
example.com/servoorders/cmd/orders: servo: 1 diagnostic(s):

transport/api/server.go:49:6: servo: *example.com/servoorders/internal/session.Session is scoped, but *example.com/servoorders/internal/transport/api.Server is a singleton that depends on it
  needed by *example.com/servoorders/internal/transport/api.Server  transport/api/server.go:49:6
  root                                           cmd/orders/spec.go:23:3

  A singleton is constructed once and held for the life of the process, so it
  would capture whichever *example.com/servoorders/internal/session.Session happened to be built first and hand that same
  one to every caller afterwards, whatever key they present. Nothing about the
  running program would say so.

  Two ways out:
    - depend on the accessor instead: change api.New's parameter from *example.com/servoorders/internal/session.Session to example.com/servoorders/internal/session.Sessions,
      and call Acquire(ctx) per request
    - make *example.com/servoorders/internal/transport/api.Server scoped too, by giving it a dependency on example.com/servoorders/internal/session.UserID
```

(Absolute paths shortened. `examples/diagnostics/widening` is the same mistake as a permanently
broken fixture, if you'd rather see it without editing this module:
`go run ./cmd/servo generate --dir examples/diagnostics/widening`.)

That is the reason this feature is in the generator rather than in a library you import. A
hand-written registry beside servo gives you the map and the timer; nothing gives you this.

Three more diagnostics guard the neighbouring mistakes — a nested scope, a `ScopeKey` whose own
dependencies are scoped, and a `ScopeKey` method with no `servo.Scoped` declaring it. All four are
in [chapter 20](20-troubleshooting.md) and in the
[Scoped instances reference](../reference/scopes.md).

## What came out

```
$ servo graph --dir examples/tutorial/cmd/orders
...
══ example.com/servoorders/internal/session.UserID ══
  linger: 5m0s   max: 50000
  accessors: example.com/servoorders/internal/session.Sessions
  borrows:   *example.com/servoorders/internal/session.Settings, *example.com/servoorders/internal/observability.Logger
── Scope level 1 ──
  *example.com/servoorders/internal/session.Session
      deps: example.com/servoorders/internal/session.UserID, *example.com/servoorders/internal/session.Settings, *example.com/servoorders/internal/observability.Logger
      capabilities: Initializer, Flusher, Finalizer
      binding: sole candidate
      pos: internal/session/session.go:81:6
```

Read the last three lines of the scope header carefully, because they're the whole model:

- **`accessors`** — what consumers depend on.
- **`borrows`** — `*session.Settings` and the logger are reached *through* the scope but aren't part
  of it. One of each, shared by every session. Not fifty thousand loggers.
- The member list is what one instance holds.

`servo explain` says the same thing per node:

```
$ servo explain --dir examples/tutorial/cmd/orders session.Session
*example.com/servoorders/internal/session.Session
  provider:     session.New (internal/session/session.go:81:6)
  binding:      sole candidate
  lifetime:     scoped — one per example.com/servoorders/internal/session.UserID, linger 5m0s, max 50000
  level:        1
  depends on:   example.com/servoorders/internal/session.UserID, *example.com/servoorders/internal/session.Settings, *example.com/servoorders/internal/observability.Logger
  depended on:  (acquired via example.com/servoorders/internal/session.Sessions)
  capabilities: Initializer, Flusher, Finalizer
```

Inside `servo_gen.go` there's now a registry, an entry type, and an accessor:

```go
type userIDScope struct {
	app    *App
	mu     sync.RWMutex
	items  map[session.UserID]*userIDEntry
	// ...
}

type sessionsAccessor struct{ s *userIDScope }

func (x sessionsAccessor) Acquire(ctx context.Context) (*session.Session, func(), error) { ... }
func (x sessionsAccessor) Stats() servo.ScopeStats                                       { ... }
```

It's ordinary Go, in a file you can step through in a debugger. It is also the only concurrent
thing servo generates, which is why it carries more comment than the rest of the output: the reader
who ends up there is debugging a race, not skimming construction order.

## Lifecycle, per instance

`Session` implements `Init`, `Flush` and `Stop`, and all three are wired — per session, not per
process:

| Phase | When |
| --- | --- |
| `New` (the constructor) | First `Acquire` of a user who has none |
| `Init` | Immediately after |
| `Flush` | Five minutes after the last request, once the linger window closes |
| `Stop` | After `Flush` |

`Flush` is where the session's contents leave memory:

```go
func (s *Session) Flush(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	slog.Info("session: closed", "user", string(s.id), "views", s.views, "recent", len(s.recent))
	return nil
}
```

The lock matters: eviction runs on the scope's own goroutine, not on a request's, so `Flush` reads
fields that a handler may still be writing.

In a real service that's where you'd persist whatever should survive the session ending. Here it's
one line, which is enough to watch the lifecycle happen.

Two things are deliberately *not* wired. `Health` and `Ready` don't cover scoped nodes — a
readiness report with one entry per logged-in user is not a report. And `Run`, if a scoped type had
one, runs on the instance's own context rather than any acquirer's, so one user disconnecting can't
kill a session someone else is still in.

## Try it yourself

```
cd examples/tutorial
make up                       # postgres, redis, nats
go run ./cmd/orders
```

In another terminal:

```
TOKEN=$(curl -s -XPOST localhost:8080/auth/login \
  -d '{"username":"alice","password":"password123"}' | jq -r .token)

curl -s localhost:8080/me/recent -H "Authorization: Bearer $TOKEN"
# {"recent":[]}

ORDER=$(curl -s -XPOST localhost:8080/orders -H "Authorization: Bearer $TOKEN" \
  -d '{"item":"widget","quantity":2}' | jq -r .id)
curl -s localhost:8080/orders/$ORDER -H "Authorization: Bearer $TOKEN" > /dev/null

curl -s localhost:8080/me/recent -H "Authorization: Bearer $TOKEN"
# {"recent":["<the order id>"]}
```

The second `/me/recent` reads state the *first* request created — with no database involved. Then
stop the service with `Ctrl-C` and watch the log:

```
{"time":"...","level":"INFO","msg":"session: closed","user":"...","views":1,"recent":1}
```

That's `Flush` running as `Shutdown` tears the scope down.

## Testing it

Two levels, and the split is worth noticing.

**The session package on its own** doesn't need servo at all — it's a struct with methods:

```go
func TestScopeKeyReadsTheContext(t *testing.T) {
	var zero *session.Session // called on a typed nil, exactly as generated code does

	got, err := zero.ScopeKey(session.WithUser(context.Background(), "alice"))
	if err != nil {
		t.Fatalf("ScopeKey: %v", err)
	}
	if got != "alice" {
		t.Fatalf("ScopeKey = %q, want alice", got)
	}

	if _, err := zero.ScopeKey(context.Background()); !errors.Is(err, servo.ErrNoScopeKey) {
		t.Fatalf("ScopeKey with no key: err = %v, want servo.ErrNoScopeKey", err)
	}
}
```

**The API package** needs *something* satisfying `session.Sessions`, and the generated accessor
lives in `package main` where `api_test` can't reach it. So the test writes its own — about
thirty lines, keyed off the very same `ScopeKey` method:

```go
type fakeSessions struct {
	settings *session.Settings
	mu       sync.Mutex
	by       map[session.UserID]*session.Session
}

func (f *fakeSessions) Acquire(ctx context.Context) (*session.Session, func(), error) {
	var zero *session.Session
	key, err := zero.ScopeKey(ctx)
	if err != nil {
		return nil, nil, err
	}
	// ...one *Session per key, no refcount, no linger
}
```

That is the payoff of depending on the interface rather than the instance: `api` is testable with
no servo, no generated code, and no concurrency — and it still gets one session per user, because
the fake keys itself the same way the real one does.

For a test that needs the real scope, `servotest.Linger(t, 0)` shrinks every linger window to zero
so eviction happens the instant the last holder releases, instead of five minutes later.

## Diagnostics

| Message | Cause | Fix |
| --- | --- | --- |
| `X is scoped, but Y is a singleton that depends on it` | A singleton took the scoped type directly | Depend on the accessor interface; `Acquire` per request |
| `X.ScopeKey must not name its receiver` | `func (s *Session) ScopeKey(...)` | Write `func (*Session) ScopeKey(...)` |
| `ScopeKey must return exactly (K, error)` | The error result was dropped | Put it back — see above for what it prevents |
| `ScopeKey's key type is string, which is not a defined type` | `ScopeKey` returns a bare `string` | `type UserID string`, and return that |
| `X declares a ScopeKey method but no servo.Scoped declares it` | The method is there, the marker isn't | Add the `servo.Scoped[T, I]` the message prints |
| `servo.Linger's argument must be a constant expression` | `servo.Linger(cfg.Something)` | The spec is read, never run. Use a constant |
| `servo: scope is at its Max live-instance cap` (at runtime) | More live keys than `Max` | Raise the cap, shorten the linger, or reject the traffic |

## Do's and don'ts

- **Do** put the key in the context at exactly one place — the middleware that authenticates the
  request. Two places is two chances for them to disagree about who the user is.
- **Do** `defer release()` on the line after `Acquire`, always. The backstop exists for the case
  where you forgot, not as an alternative to remembering.
- **Do** pick `Linger` from how long a gap between two requests should still count as the same
  session. Zero is a real answer — it means "die with the last holder" — just not a default to
  fall into.
- **Don't** store an acquired instance in a struct field that outlives the call. That's the
  widening bug by hand, in a place servo can't see it: the check covers constructor parameters,
  not assignments you make afterwards.
- **Don't** reach for a scope when an eager singleton would do. Two known regions, an A/B pair, a
  primary and a replica — those are distinct *types* and belong in the previous chapter's model
  (see the README's "Multiple instances of the same type"). A scope earns its keep when the key
  space is open and comes from outside.
- **Don't** expect a scope to coordinate across processes. Two replicas means two sessions per
  user unless your routing is sticky. That's documented, not solved.

## Next

[Chapter 15: Observability](15-observability.md) — structured logs, metrics, and tracing, now that
there's a fully wired app, sessions and all, to instrument.
