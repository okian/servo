# servotest package

```go
import "github.com/okian/servo/v3/servotest"
```

**Who this is for:** anyone writing a test against a generated `App` or `TestApp`.

Six small helpers, each addressing something that is awkward to check by hand: goroutine leaks,
real init/stop ordering, the abandoned-node path, the eviction-racing-acquire boundary of a scope's
linger window, and giving `gomock` a reporter inside a graph that has no `*testing.T` in it.

This is the only servo package with a third-party dependency —
[`go.uber.org/goleak`](https://pkg.go.dev/go.uber.org/goleak), used by `NoLeaks`. The core
(`cmd/servo`, the internals) depends on nothing beyond `golang.org/x/tools`.

## Index

| Identifier | Kind | |
| --- | --- | --- |
| [`NoLeaks`](#noleaks) | func | Fail if any goroutine outlives the test |
| [`Linger`](#linger) | func | Shrinks every scope's linger window for one test |
| [`Timeout`](#timeout) | func | Shrink `servo.DefaultStopBudget` for one test |
| [`Recorder`](#recorder) | type | An app's init and shutdown reports, together |
| [`NewRecorder`](#newrecorder) | func | Builds a `Recorder` |
| [`AssertInitOrder`](#assertinitorder-and-assertstoporder) | func | Assert init ordering |
| [`AssertStopOrder`](#assertinitorder-and-assertstoporder) | func | Assert stop ordering |
| [`PanicReporter`](#panicreporter) | type | A `gomock.TestReporter` with no `*testing.T` |

## `NoLeaks`

```go
func NoLeaks(t *testing.T)
```

Fails `t` if any goroutine started during the test is still running when it returns. Use it as the
first `defer` in the test:

```go
func TestApp(t *testing.T) {
	defer servotest.NoLeaks(t)
	// ...
}
```

This is a meaningful check for a servo app specifically, because `Run` launches goroutines and
`Shutdown` is supposed to end them. A leak here usually means either a `Runner` that ignores context
cancellation, or a node that was **abandoned** — its stop call blew its budget and its goroutine was
left running by design. That second case is a real finding, not a false positive: the report will
say `abandoned` and this is the assertion that stops it going unnoticed.

Servo's own suite uses it; it's exported so generated-app tests can too.

## `Timeout`

```go
func Timeout(t *testing.T, d time.Duration)
```

Sets [`servo.DefaultStopBudget`](servo-package.md#defaultstopbudget) to `d` for the calling test and
restores the previous value via `t.Cleanup`.

Its purpose is exercising the abandoned-node path without a slow suite. A component that
deliberately blocks in `Stop` would otherwise cost the real 5-second budget per test:

```go
servotest.Timeout(t, 50*time.Millisecond)
```

**Tests using it must not run in parallel** — with each other, or with any test that depends on the
real default. The budget is a package variable, so `t.Parallel()` plus `Timeout` is a data race and a
flake. That's the cost of the budget being a variable rather than configuration, and it's a fair
trade for tests that would otherwise take seconds each.

## `Linger`

```go
func Linger(t *testing.T, d time.Duration)
```

Overrides every generated [scope](scopes.md)'s declared linger window for the calling test,
restoring the previous setting via `t.Cleanup`.

It exists for the same reason `Timeout` does: the interesting behaviour lives at a boundary a real
thirty-second window cannot be driven to in a test.

```go
func TestEvictionRacingAcquire(t *testing.T) {
	servotest.Linger(t, 0) // die with the last holder
	app, _ := New(ctx)
	// …hammer one key from several goroutines; every acquire must end in
	// an instance or a clean error, never a hang.
}
```

`Linger(t, 0)` makes an instance evict the moment its last holder releases, which is how the
eviction-racing-acquire path gets exercised deliberately instead of by luck.

Generated code reads the override once per scope, inside `New`, so **call this before constructing
the app**. Since the underlying setting is a package variable, tests using `Linger` must not run in
parallel with each other or with tests that depend on a scope's real declared window — the same
constraint `Timeout` carries, for the same reason.

## `Recorder`

```go
type Recorder struct {
	Init     servo.StartupReport
	Shutdown servo.Report
}
```

An app's own init and shutdown reports, side by side. Nothing is instrumented to produce them: the
generated `New` already records which nodes initialised and how long each took, and `Shutdown`
already lists nodes in the order it actually stopped them. `Recorder` just holds both so ordering can
be asserted against what happened rather than against a re-derivation of what should have happened.

### `NewRecorder`

```go
func NewRecorder(init servo.StartupReport, shutdown servo.Report) *Recorder
```

Call `app.Report()` right after `New`/`NewTestApp` succeeds, and `app.Shutdown` after driving the app
through its test:

```go
rec := servotest.NewRecorder(app.Report(), app.Shutdown(ctx))
```

## `AssertInitOrder` and `AssertStopOrder`

```go
func AssertInitOrder(t *testing.T, rec *Recorder, want ...string)
func AssertStopOrder(t *testing.T, rec *Recorder, want ...string)
```

Fail `t` unless `want` appears, **in order, as a subsequence** of what actually happened. Other nodes
may be interspersed:

```go
servotest.AssertStopOrder(t, rec, "*api.Server", "*postgres.DB")
```

That asserts the server stopped before the database. It does not assert that nothing else stopped
between them, and it does not require naming every node — which is the point. A relative guarantee
("the thing that uses the database stops first") is what servo actually promises, and what stays
true when you add an unrelated component to the graph. On failure:

```
stop order [*api.Server *postgres.DB *logger.Logger] does not contain [*postgres.DB *api.Server] as a subsequence
```

Names are the node's full type string — the same identity used everywhere else. In a real module
that's the fully qualified form, `*example.com/app/api.Server`; `servo graph` or the generated file's
header comment is the quickest way to copy the exact strings.

`AssertInitOrder` reads `Recorder.Init` (from `App.Report()`), `AssertStopOrder` reads
`Recorder.Shutdown`. Two caveats for init ordering: only nodes implementing `Initializer` appear at
all, and nodes sharing a [level](resolution.md#construction-order-and-levels) ran concurrently, so
their relative order is completion order and must not be asserted. Assert across levels, not within
one.

## `PanicReporter`

```go
type PanicReporter struct{}

func (PanicReporter) Errorf(format string, args ...any)
func (PanicReporter) Fatalf(format string, args ...any)
```

Satisfies [`gomock`](https://pkg.go.dev/go.uber.org/mock/gomock)'s `TestReporter` interface
structurally — without `servotest` importing gomock at all. Both methods panic with the formatted
message.

It exists because `gomock.NewController` needs a reporter and there is no `*testing.T` reachable from
inside a generated graph. A gomock adapter's constructor supplies one:

```go
func NewMockStoreForServo() *MockStoreForServo {
	ctrl := gomock.NewController(servotest.PanicReporter{})
	return &MockStoreForServo{MockStore: NewMockStore(ctrl), Finish: ctrl.Finish}
}
```

**Be clear about the trade-off.** A failed expectation panics rather than calling `t.Fatalf`. Go's
test runner still reports which test failed before the panic propagates, but the process exits —
unlike a clean, isolated failure. And `ctrl.Finish` must be called directly by the test with a plain
`defer`, never routed through servo's cleanup-func shape, which would run it inside
[`servo.RunStop`](servo-package.md#runstop)'s goroutine during `Shutdown` where nothing can recover
the panic.

For strict expectation-count verification, construct and drive the mock directly in an isolated unit
test with a real `gomock.NewController(t)` instead of going through `servo.Override`. Graph injection
is the right tool for exercising the wiring; it is not the right tool for gomock's stricter
verification style. The README's
[gomock section](https://github.com/okian/servo/blob/master/README.md#gouberorgmock-gomock) has the
full reasoning, and
[`examples/mocking/gomock`](https://github.com/okian/servo/tree/master/examples/mocking/gomock) is a
working version.

## A complete test

Everything above, in the shape it's usually used:

```go
func TestApp(t *testing.T) {
	defer servotest.NoLeaks(t)                // clean by construction, or the test says so
	servotest.Timeout(t, 50*time.Millisecond) // don't pay 5s per abandoned node

	ctx := context.Background()
	app, err := NewTestApp(ctx) // generated because the spec declares servo.Override
	if err != nil {
		t.Fatal(err)
	}

	app.storeMock.GetFunc = func(key string) string { return "mocked:" + key }
	if got := app.server.Lookup("user:42"); got != "mocked:user:42" {
		t.Fatalf("got %q", got)
	}

	rec := servotest.NewRecorder(app.Report(), app.Shutdown(ctx))
	servotest.AssertStopOrder(t, rec, "*api.Server", "*mockstore.Store")
}
```
