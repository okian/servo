# HTTP Groups, Middleware & Extractors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Multiple listeners per app (route groups → ports), wrap middleware at server/group/route level, and typed per-request handler parameters (extractors).

**Architecture:** Extends the shipped `//servo:` HTTP feature along its own seams: the directive gains an optional group token (`internal/route`), `servo.HTTP` gains `Group`/`Use`/`Route` options and `Build` gains `Extract[T]` (`servo` + `internal/load`), resolve validates middleware/extractor shapes and filters routes by the injector's served groups (`internal/resolve`), and emit produces one server per group with middleware chains and extractor calls (`internal/emit`). Everything stays generate-time checked; the only runtime check is the `Groups` map lookup (port numbers are runtime values).

**Tech Stack:** Go 1.27, stdlib only (`go/ast`, `go/types`, `net/http`); `golang.org/x/tools` in the generator.

**Spec:** `docs/superpowers/specs/2026-09-05-http-groups-middleware-design.md`

## Global Constraints

- The `servo` runtime package imports stdlib only; the generator (`internal/*`, `cmd/servo`) depends on nothing beyond `golang.org/x/tools`.
- An app with no `servo.HTTP()` must emit a byte-identical file (existing goldens `fullapp.go.golden` / `scopedapp.go.golden` must not change).
- The previous single-group HTTP feature is **unreleased**: changing `servo.HTTP`'s signature and the emitted single-group output is allowed (refresh `httpapp.go.golden` deliberately, reviewing the diff).
- Every diagnostic carries a `token.Position` and starts with `servo: `; message text is pinned by exact-substring tests.
- All emitted identifiers come from the existing allocators (`e.types`, `e.names`, `allocateAppField`, `imports.Reserve`); never hard-code a name a user package could shadow.
- Group name grammar everywhere: `^[A-Za-z0-9_-]+$`; the name `default` is reserved (implicit) and rejected in `Group(...)` **declarations** but accepted in `Use` selectors and directive tokens.
- Run `gofmt -l .`, `go vet ./...` and the package's tests before every commit. Branch: create `http-groups` from `master` before Task 1.

---

### Task 1: Runtime — `HTTPListener` and `HTTPConfig.Groups`

**Files:**
- Modify: `servo/httpconfig.go`
- Test: `servo/httpconfig_test.go` (create)

**Interfaces:**
- Produces: `servo.HTTPListener{IP string; Port uint16; CertFile, KeyFile string; MaxBodyBytes int64; ReadTimeout, WriteTimeout, IdleTimeout time.Duration}` and field `HTTPConfig.Groups map[string]HTTPListener`. Task 6's emitted code reads `cfg.Groups["<name>"]` for named groups and the existing flat fields for the default group.

- [ ] **Step 1: Write the failing test**

```go
package servo

import (
	"testing"
	"time"
)

// The emitted server reads these fields by name; renaming any of them is a
// contract change this test is meant to catch.
func TestHTTPConfigGroupListeners(t *testing.T) {
	cfg := HTTPConfig{
		IP: "0.0.0.0", Port: 9000,
		Groups: map[string]HTTPListener{
			"telemetry": {IP: "127.0.0.1", Port: 9001, MaxBodyBytes: 1 << 16, ReadTimeout: time.Second},
			"internal":  {Port: 9002, CertFile: "c.pem", KeyFile: "k.pem"},
		},
	}
	if cfg.Groups["telemetry"].Port != 9001 || cfg.Groups["internal"].CertFile != "c.pem" {
		t.Fatalf("group listeners = %+v", cfg.Groups)
	}
}
```

- [ ] **Step 2: Run `go test ./servo/ -run TestHTTPConfigGroupListeners`** — expected FAIL: `undefined: HTTPListener`.

- [ ] **Step 3: Implement** — in `servo/httpconfig.go`, add above `HTTPConfig`:

```go
// HTTPListener is one named group's listener. The default group's listener
// is HTTPConfig's own flat fields; every group declared with servo.Group
// needs an entry in HTTPConfig.Groups, checked when the App is constructed.
type HTTPListener struct {
	// IP is the interface to bind; empty means all interfaces.
	IP   string
	Port uint16
	// CertFile and KeyFile enable TLS when both are set.
	CertFile string
	KeyFile  string
	// MaxBodyBytes bounds request bodies; <= 0 uses the generated 1 MiB default.
	MaxBodyBytes int64
	ReadTimeout, WriteTimeout, IdleTimeout time.Duration
}
```

and add to `HTTPConfig` (after the timeout fields):

```go
	// Groups holds the listener for each group declared with
	// servo.Group("name") — one extra port per entry. The zero map is a
	// valid config for an app that declares no groups.
	Groups map[string]HTTPListener
```

- [ ] **Step 4: Run `go test ./servo/`** — expected PASS (whole package).
- [ ] **Step 5: Commit** — `git add servo/ && git commit -m "Add named group listeners to HTTPConfig"`

---

### Task 2: Markers — `HTTPOption`, `HTTP(...)`, `Group`, `Use`, `Route`, `Extract` + servo-vet

**Files:**
- Modify: `servo/markers.go`, `servo/markers_test.go`, `cmd/servo-vet/main.go`, `cmd/servo-vet/main_test.go`

**Interfaces:**
- Produces (all panic; parsed as syntax by Task 3):

```go
type HTTPOption struct{}
func HTTP(...HTTPOption) Marker          // signature change from HTTP()
func Group(name string) HTTPOption
func Use[T any](...HTTPOption) HTTPOption
func Route(pattern string) HTTPOption
func Extract[T any]() Marker
```

- [ ] **Step 1: Write the failing tests** — append to `servo/markers_test.go`:

```go
func TestGroupPanics(t *testing.T) {
	expectPanic(t, "servo: Group executed at runtime", func() { Group("telemetry") })
}

func TestUsePanics(t *testing.T) {
	expectPanic(t, "servo: Use executed at runtime", func() { Use[int]() })
}

func TestRoutePanics(t *testing.T) {
	expectPanic(t, "servo: Route executed at runtime", func() { Route("GET /x") })
}

func TestExtractPanics(t *testing.T) {
	expectPanic(t, "servo: Extract executed at runtime", func() { Extract[int]() })
}
```

and to `cmd/servo-vet/main_test.go` (same shape as `TestFlagsHTTPMarkerCall`):

```go
func TestFlagsGroupUseRouteExtractMarkerCalls(t *testing.T) {
	const src = `package fixture

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.HTTP(
			servo.Group("telemetry"),
			servo.Use[int](servo.Route("GET /x")),
		),
		servo.Extract[int](),
	)
}
`
	got := runOn(t, src)
	// Build, HTTP, Group, Use, Route, Extract — six calls, six diagnostics.
	if len(got) != 6 {
		t.Fatalf("got %d diagnostics, want 6: %v", len(got), got)
	}
}
```

- [ ] **Step 2: Run both** — `go test ./servo/ -run 'TestGroupPanics|TestUsePanics|TestRoutePanics|TestExtractPanics'` and `go test ./cmd/servo-vet/ -run TestFlagsGroupUseRouteExtract` — expected FAIL (`undefined: Group` / wrong diagnostic count).

- [ ] **Step 3: Implement** — in `servo/markers.go`: change `HTTP()` to `HTTP(...HTTPOption)` (same body, doc comment gains a sentence: "Options declare extra listener groups and attach middleware; see Group, Use and Route."), and add:

```go
// HTTPOption is the opaque return type of Group, Use and Route — read as
// syntax inside servo.HTTP's argument list, never executed.
type HTTPOption struct{}

// Group, inside servo.HTTP(...), declares a named listener group: routes
// carrying this name as their directive's trailing token are served on the
// listener HTTPConfig.Groups[name] describes. Inside servo.Use(...), it
// selects the group the middleware wraps. The default group needs no
// declaration; "default" names it in a Use selector.
func Group(name string) HTTPOption {
	panic("servo: Group executed at runtime — run `servo generate`")
}

// Use attaches middleware: T is a graph node whose
// Middleware(next http.Handler) http.Handler method wraps the emitted
// server. No selector wraps every group; Group selectors wrap one group's
// whole mux; Route selectors wrap single routes. Declaration order is
// outermost-first, stacked server → group → route.
func Use[T any](...HTTPOption) HTTPOption {
	panic("servo: Use executed at runtime — run `servo generate`")
}

// Route, inside servo.Use(...), selects one route by its full
// "METHOD /pattern" spelling. The pattern must match a served route
// exactly — checked at generate time.
func Route(pattern string) HTTPOption {
	panic("servo: Route executed at runtime — run `servo generate`")
}

// Extract declares T as an extractor: its Extract(r *http.Request) (V, error)
// method makes every //servo: handler parameter of type V a per-request
// value produced from the request, instead of a graph dependency. An
// extraction error short-circuits through the same status contract as a
// handler error.
func Extract[T any]() Marker {
	panic("servo: Extract executed at runtime — run `servo generate`")
}
```

In `cmd/servo-vet/main.go` extend `markerNames` with `"Group": true, "Use": true, "Route": true, "Extract": true`.

- [ ] **Step 4: Run** the same tests — expected PASS; then `go build ./... && go test ./servo/ ./cmd/servo-vet/`.
  Note: `examples/http/cmd/app/spec.go` still compiles (`servo.HTTP()` with zero variadic args is fine).
- [ ] **Step 5: Commit** — `git commit -am "Add Group/Use/Route/Extract markers; HTTP takes options"`

---

### Task 3: Spec parsing — `HTTPDecl` grows, `Spec.Extracts`, constant-string folding

**Files:**
- Modify: `internal/load/spec.go`
- Test: `internal/load/spec_test.go` (append)

**Interfaces:**
- Produces (consumed by Tasks 5/7):

```go
type HTTPDecl struct {
	Pos    token.Position
	Groups []GroupDecl
	Uses   []UseDecl
}
type GroupDecl struct{ Name string; Pos token.Position }
type UseDecl struct {
	Type   graph.Key   // T in Use[T]
	TypeT  types.Type
	Groups []string    // Group selectors ("default" allowed)
	Routes []RouteSel  // Route selectors
	Pos    token.Position
}
type RouteSel struct{ Pattern string; Pos token.Position } // "POST /order/{category}/"
type ExtractDecl struct {
	Type  graph.Key
	TypeT types.Type
	Pos   token.Position
}
// Spec gains: Extracts []ExtractDecl
```

- [ ] **Step 1: Write the failing tests** — append to `internal/load/spec_test.go`, temp-module style like `TestFindSpecAcceptsHTTPMarker`. One acceptance test and one table of rejections. The acceptance fixture's spec:

```go
//go:build servoinject

package spec

import (
	"net/http"

	"example.com/httpopts/mw"
	"github.com/okian/servo/v3/servo"
)

var _ = http.MethodGet

func Wire() {
	servo.Build(
		servo.HTTP(
			servo.Group("telemetry"),
			servo.Group("internal"),
			servo.Use[*mw.Recover](),
			servo.Use[*mw.Auth](servo.Group("internal")),
			servo.Use[*mw.Audit](servo.Route("POST /order/{category}/")),
		),
		servo.Extract[*mw.UserExtractor](),
	)
}
```

with `mw/mw.go` providing the four types (plain structs; methods are irrelevant at parse time —
give each a `Middleware(next http.Handler) http.Handler` / `Extract(r *http.Request) (*User, error)`
method anyway so later tasks can reuse the fixture). Assertions:

```go
spec.HTTP.Groups        // [{telemetry ...} {internal ...}] in order
spec.HTTP.Uses          // 3 entries, declaration order
spec.HTTP.Uses[0].Type.String() == "*example.com/httpopts/mw.Recover" && no selectors
spec.HTTP.Uses[1].Groups == []string{"internal"}
spec.HTTP.Uses[2].Routes[0].Pattern == "POST /order/{category}/"
spec.Extracts[0].Type.String() == "*example.com/httpopts/mw.UserExtractor"
```

Rejection table (each its own temp module, `strings.Contains` on the error):

| spec fragment | want substring |
|---|---|
| `servo.Group("x")` at Build top level | `servo.Group belongs inside servo.HTTP(...)` |
| `servo.Use[*mw.Recover]()` at Build top level | `servo.Use belongs inside servo.HTTP(...)` |
| `servo.Route("GET /x")` anywhere but inside Use | `servo.Route belongs inside servo.Use(...)` |
| `servo.HTTP(servo.Group("default"))` | `"default" is the implicit group` |
| `servo.HTTP(servo.Group("bad name"))` | `must match [A-Za-z0-9_-]+` |
| `servo.HTTP(servo.Group("x"), servo.Group("x"))` | `servo.Group("x") declared twice` |
| `servo.HTTP(servo.Use[*mw.Auth](servo.Group("a"), servo.Route("GET /x")))` | `selects groups or routes, not both` |
| `servo.Group(name)` where `name` is a variable | `must be a constant string` |
| `servo.Extract[*mw.UserExtractor](), servo.Extract[*mw.UserExtractor]()` | `servo.Extract[*example.com/httpopts/mw.UserExtractor] declared twice` |
| `servo.HTTP(servo.Root[int]())` | `not a servo.HTTP option` |

- [ ] **Step 2: Run `go test ./internal/load/ -run TestFindSpecHTTPOptions`** (and the reject test) — expected FAIL.

- [ ] **Step 3: Implement in `internal/load/spec.go`:**
  - Add the types from the Interfaces block; add `Extracts []ExtractDecl` to `Spec`.
  - Add a constant-string helper:

```go
// constStringArg folds call.Args[i] to its constant string value — the spec
// is read as syntax, so a variable or call here has no value to read.
func constStringArg(pkg *packages.Package, call *ast.CallExpr, i int, what string) (string, error) {
	pos := pkg.Fset.Position(call.Pos())
	if len(call.Args) != 1 {
		return "", fmt.Errorf("%s: %s expects exactly one argument", pos, what)
	}
	tv, ok := pkg.TypesInfo.Types[call.Args[i]]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", fmt.Errorf("%s: %s's argument must be a constant string", pos, what)
	}
	return constant.StringVal(tv.Value), nil
}
```

  - In `parseBuildCall`: replace `case "HTTP"` with a call to a new `parseHTTPCall(pkg, argCall, pos)` returning `*HTTPDecl`; add `case "Extract"` (one type arg required; duplicate by `Type` key → `declared twice` with the first position); add `case "Group", "Use":` → `servo.%s belongs inside servo.HTTP(...)`; add `"Route"` → `belongs inside servo.Use(...)`.
  - `parseHTTPCall` walks `argCall.Args`; each must be a `*ast.CallExpr` resolved via `markerCall`; `Group` → validate with `var groupNameRE = regexp.MustCompile("^[A-Za-z0-9_-]+$")`, reject `default` (`%s: servo.Group("default") — "default" is the implicit group every servo.HTTP() injector serves; it needs no declaration`), reject dupes; `Use` → `parseUseCall` (type arg count 1; walk its args: `Group` selector appends to `Groups` — `default` allowed, still regexp-checked; `Route` selector via `constStringArg` appends `RouteSel`; anything else → `%s: servo.%s is not a servo.Use selector — use servo.Group("name") or servo.Route("METHOD /pattern")`; both kinds present → `%s: servo.Use[%s] selects groups or routes, not both — split it into two Use calls`); anything else → `%s: servo.%s is not a servo.HTTP option — options are servo.Group and servo.Use`.
  - Import `go/constant` and `regexp`.

- [ ] **Step 4: Run `go test ./internal/load/`** — expected PASS (including all pre-existing tests: `TestFindSpecAcceptsHTTPMarker` still passes because `HTTP()` with no args yields empty `Groups`/`Uses`).
- [ ] **Step 5: Commit** — `git commit -am "Parse Group/Use/Route options and Extract declarations"`

---

### Task 4: Route scan — the group token

**Files:**
- Modify: `internal/route/route.go` (add `Group string` to `Route`), `internal/route/scan.go`
- Test: `internal/route/scan_test.go` (append)

**Interfaces:**
- Produces: `route.Route.Group` — `""` for the default group (a literal `default` token normalizes to `""`). Dedup and the conflict probe run **per group**.

- [ ] **Step 1: Write the failing tests** — append:

```go
func TestScanGroupToken(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:get /healthz telemetry
func Healthz(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /orders default
func Orders(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /plain
func Plain(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	routes, diags := scanOn(t, src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	byName := map[string]string{}
	for _, rt := range routes {
		byName[rt.Func.Name()] = rt.Group
	}
	if byName["Healthz"] != "telemetry" || byName["Orders"] != "" || byName["Plain"] != "" {
		t.Fatalf("groups = %v", byName)
	}
}

// The same method+pattern on two different groups is two different servers
// — legal. On one group it is still a duplicate.
func TestScanDuplicatesArePerGroup(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:post /order
func A(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /order internal
func B(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	routes, diags := scanOn(t, src)
	if len(diags) != 0 || len(routes) != 2 {
		t.Fatalf("routes=%d diags=%v, want 2 routes and no diagnostics", len(routes), diags)
	}
}

func TestScanRejectsBadGroupToken(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:get /x bad group
func X(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	_, diags := scanOn(t, src)
	if len(diags) == 0 || !strings.Contains(diags[0].Message, "unexpected \"group\"") {
		t.Fatalf("diags = %v", diags)
	}
}
```

Also add a per-group conflict case (`GET /a/{x}` default and `GET /a/{y}` on `internal` → **no** diagnostic; both on one group stays a diagnostic — extend `TestScanDiagnostics`' existing "mux pattern conflict" expectations only if needed).

- [ ] **Step 2: Run `go test ./internal/route/ -run 'TestScanGroupToken|TestScanDuplicatesArePerGroup|TestScanRejectsBadGroupToken'`** — expected FAIL (`rt.Group` undefined).

- [ ] **Step 3: Implement:**
  - `route.go`: add `// Group is the trailing directive token, "" for the default group.` `Group string` to `Route`.
  - `scan.go` `parseDirective`: accept an optional third field — grammar `^[A-Za-z0-9_-]+$` (share `groupTokenRE`); `default` → `""`; a fourth field → reuse the "takes exactly one pattern" message extended: `servo: //servo:%s takes a pattern and an optional group — unexpected %q after %q`. A third token failing the grammar: `servo: group %q must match [A-Za-z0-9_-]+`.
  - `directive` struct gains `group string`; `scanFile` copies it onto the `Route`.
  - Sorting: key becomes `(Group, Pattern, Method, Pos)`.
  - `dedupAndProbe`: key `rt.Group + "\x00" + rt.Method + " " + rt.Pattern` for dedup; one `http.NewServeMux()` **per group** (map `map[string]*http.ServeMux`, lazily created) for the conflict probe.

- [ ] **Step 4: Run `go test ./internal/route/`** — expected PASS.
- [ ] **Step 5: Commit** — `git commit -am "Scan the directive's optional group token"`

---

### Task 5: Resolve — served groups, middleware validation, extractors

**Files:**
- Modify: `internal/resolve/http.go`
- Test: `internal/resolve/http_test.go` (extend the fixture + append tests)

**Interfaces:**
- `HTTPInput` gains `Groups []load.GroupDecl`, `Uses []load.UseDecl`, `Extracts []load.ExtractDecl`.
- `HTTPPlan` becomes:

```go
type HTTPPlan struct {
	Pos        token.Position
	Config     *Node
	Groups     []string      // served non-default groups, sorted
	Routes     []*HTTPRoute  // only routes in served groups, scan order
	Uses       []*HTTPUse    // declaration order
	Extractors []*HTTPExtractor
}
type HTTPRoute struct {
	Route *route.Route
	Args  []HTTPArg // one per handler param after ctx/req, in order
}
type HTTPArg struct {
	Node      *Node          // graph dependency; nil when extracted
	Extractor *HTTPExtractor // non-nil when extracted
}
type HTTPUse struct {
	Node *Node
	Decl load.UseDecl
}
type HTTPExtractor struct {
	Node         *Node
	Produces     graph.Key
	ProducesType types.Type
}
```

- [ ] **Step 1: Extend the fixture** (`httpAppSrc` in `http_test.go`) with:

```go
type User struct{ Name string }

type UserExtractor struct{}
func NewUserExtractor() *UserExtractor { return &UserExtractor{} }
func (e *UserExtractor) Extract(r *http.Request) (*User, error) { return &User{}, nil }

type BadExtractor struct{}
func NewBadExtractor() *BadExtractor { return &BadExtractor{} }
func (e *BadExtractor) Extract(name string) (*User, error) { return nil, nil }

type Recover struct{}
func NewRecover() *Recover { return &Recover{} }
func (m *Recover) Middleware(next http.Handler) http.Handler { return next }

type NotMiddleware struct{}
func NewNotMiddleware() *NotMiddleware { return &NotMiddleware{} }

//servo:get /me2
func Me2(ctx context.Context, u *User) (servo.Json[*OrderResp], error) { return nil, nil }

//servo:get /healthz telemetry
func Healthz(ctx context.Context) (servo.Json[*OrderResp], error) { return nil, nil }
```

(the fixture file must now import `net/http`.) Then write failing tests, each building `Input` like the existing ones:

```go
// Group filtering: with no Groups declared, the telemetry route is not in
// the plan; with GroupDecl{Name:"telemetry"} it is, and Plan.Groups lists it.
func TestResolveHTTPFiltersUndeclaredGroups(t *testing.T)

// Extractor happy path: Extracts=[{*UserExtractor}], route httpapp.Me2 —
// plan route Args[0].Extractor != nil, .Produces == "*example.com/httpapp.User",
// extractor node present in resolved.Order.
func TestResolveHTTPExtractor(t *testing.T)

// Extract method with the wrong shape → diagnostic containing
// "must have a method Extract(r *http.Request) (T, error)".
func TestResolveHTTPRejectsBadExtractor(t *testing.T)

// A type that is both extracted and graph-provided → diagnostic containing
// "either constructed once or extracted per request, never both".
// (Add `func NewUser() *User { return &User{} }` to the fixture and include
// it in candidates for this test only.)
func TestResolveHTTPRejectsExtractedAndProvided(t *testing.T)

// Middleware happy path: Uses=[{Type:*Recover}] → plan.Uses[0].Node resolves.
// Missing method → "must have a method Middleware(next http.Handler) http.Handler".
func TestResolveHTTPMiddleware(t *testing.T)
func TestResolveHTTPRejectsNonMiddleware(t *testing.T)

// Selector validation: Use with Groups=["nope"] → `servo.Use selects group "nope",
// but this injector serves`; Use with Routes=["POST /missing"] →
// "matches no served route".
func TestResolveHTTPRejectsUnknownUseSelectors(t *testing.T)
```

- [ ] **Step 2: Run `go test ./internal/resolve/ -run TestResolveHTTP`** — expected FAIL (unknown fields).

- [ ] **Step 3: Implement in `internal/resolve/http.go`:**
  - Served set: `served := map[string]bool{"": true}` plus each `GroupDecl.Name`; `plan.Groups` = sorted declared names. Filter `in.Routes` by `served[rt.Group]` before dep resolution.
  - Extractors first (their produced keys change param classification):

```go
for _, d := range in.Extracts {
	node, ok := r.resolveKey(d.Type, d.TypeT, []chainEntry{{Label: "extractor " + d.Type.String() + " (servo.Extract)", Pos: d.Pos}}, in.Pos)
	if !ok { continue }
	if node.Scoped() { /* scopedHandlerDepDiagnostic-style message for extractors */ continue }
	produces, diag := extractMethodResult(d.TypeT, d.Pos) // validates the method shape
	if diag != nil { r.diags = append(r.diags, *diag); continue }
	key := graph.NewKey(produces, "")
	if prior, dup := extractorByKey[key]; dup { /* "two extractors produce %s — first declared by %s" */ }
	if len(r.byResult[key]) > 0 {
		// name the provider: "%s has both a provider (%s at %s) and an extractor (%s) —
		// a type is either constructed once or extracted per request, never both"
	}
	ex := &HTTPExtractor{Node: node, Produces: key, ProducesType: produces}
	extractorByKey[key] = ex
	plan.Extractors = append(plan.Extractors, ex)
	if _, seen := r.rootPos[node]; !seen { r.rootPos[node] = d.Pos }
}
```

  - `extractMethodResult` uses `types.LookupFieldOrMethod(t, true, nil, "Extract")`; requires: exactly one param, a pointer to named `net/http.Request` (check `named.Obj().Pkg().Path() == "net/http" && named.Obj().Name() == "Request"`); results exactly `(V, error)` with `V` named or pointer-to-named. Failure message: `servo: %s must have a method Extract(r *http.Request) (T, error) to be declared with servo.Extract — %s` with a sub-detail (no method / wrong params / wrong results).
  - Middleware: same pattern, method `Middleware`, exactly one param and one result, both the named interface `net/http.Handler`. Message: `servo: %s must have a method Middleware(next http.Handler) http.Handler to be attached with servo.Use`. Scoped T rejected. Selector validation: each `UseDecl.Groups` entry must be `default` or in `served` (message: `servo: servo.Use[%s] selects group %q, but this injector serves only: default, %s`); each `RouteSel.Pattern` must equal `rt.Method+" "+rt.Pattern` of a served route (message: `servo: servo.Use[%s]'s selector %q matches no served route`).
  - Handler args: replace the deps loop with classification —

```go
for i, depKey := range rt.Deps {
	if ex, ok := extractorByKey[depKey]; ok {
		hr.Args = append(hr.Args, HTTPArg{Extractor: ex})
		continue
	}
	node, ok := r.resolveKey(depKey, rt.DepTypes[i], rchain, in.Pos)
	// ... existing failure hint + scoped-dep rejection unchanged ...
	hr.Args = append(hr.Args, HTTPArg{Node: node})
}
```

  (Keep the old `Deps []*Node` field removed — update `internal/emit/http.go` compile errors in Task 6, not here; to keep this task green, leave a temporary `func (hr *HTTPRoute) depNodes() []*Node` OUT — instead update emit's `re.R.Deps` references to `re.R.Args` in this same task with minimal mechanical changes so the tree compiles: `for _, arg := range re.R.Args { if arg.Node != nil { ... } }`. Emit behavior for extractors lands in Task 6; here extracted args may simply not occur in emit's existing tests.)

- [ ] **Step 4: Run `go test ./internal/resolve/ ./internal/emit/ ./cmd/servo/`** — expected PASS (emit fixtures declare no extractors, so `Args[i].Node` is always set there).
- [ ] **Step 5: Commit** — `git commit -am "Resolve served groups, middleware and extractors"`

---

### Task 6: Emit — one server per group, middleware chains, extractor calls

**Files:**
- Modify: `internal/emit/http.go`, `internal/emit/lifecycle.go` (Run/Shutdown/Ready loops over groups)
- Test: `internal/emit/http_test.go` (extend fixture + tests), refresh `internal/emit/testdata/golden/httpapp.go.golden`

**Interfaces:**
- Consumes `resolve.HTTPPlan` from Task 5.
- Emitted naming: default group keeps `httpServer`/`newHttpServer`/field `httpServer`; group `telemetry` gets type `httpTelemetryServer`, ctor `newHttpTelemetryServer`, App field `httpTelemetryServer` (base: `"http" + capitalize(name) + "Server"`, all through the existing allocators). All group ctors return `(*T, error)`. Shared package-level emitted helpers replace the per-server write methods: `httpWriteJSON(w, code, payload)`, `httpWriteError(w, code, msg)`, `httpWriteFailure(w, handler, route string, err error)` (the non-2xx status mapping + slog), and the single generic `httpRespond[T any](w http.ResponseWriter, code int, res servo.Json[T])` (no server receiver — it only needs `httpWriteJSON`).

- [ ] **Step 1: Extend the emit fixture** with a telemetry-group route, a group declaration, one middleware at each level, and an extractor (mirror Task 5's fixture additions; build the `Input` with `Groups`, `Uses`, `Extracts` populated). Write failing substring tests:

```go
// multi-group
"type httpTelemetryServer struct",
`mux.HandleFunc("GET /healthz", s.handleHealthz)`,          // only in the telemetry ctor
`cfg.Groups["telemetry"]`,                                   // named listener lookup
`fmt.Errorf("http: group %q declared in the spec but missing from HTTPConfig.Groups", "telemetry")`,
// middleware
"a.recover.Middleware(handler)",                             // server-level, outermost
"a.auth.Middleware(handler)",                                // group-level
"a.audit.Middleware(h)",                                     // route-level wrap before mux.Handle
"mux.Handle(\"POST /order/{category}/\", h)",
// extractor
"user, err := s.app.userExtractor.Extract(r)",
"httpWriteFailure(w,",                                        // shared failure path
// lifecycle
`servo.RunStop(ctx, servo.DefaultStopBudget, "http:telemetry"`,
`Name: "http:telemetry"`,                                     // Ready entry
```

plus an order assertion: in the emitted default-group ctor, `recover.Middleware` is applied **after** (i.e. wraps) `auth`… (`strings.Index` comparisons on the ctor body), and `go test ./internal/emit/ -run TestEmitHTTP` first to watch the new assertions FAIL.

- [ ] **Step 2: Run — expected FAIL** on every new substring.

- [ ] **Step 3: Implement in `internal/emit/http.go`:**
  - `httpEmit` becomes a list: `Groups []*httpGroupEmit` where

```go
type httpGroupEmit struct {
	Name       string // "" = default
	ServerType, NewFunc, Field, StopMethod string
	Routes     []*httpRouteEmit
	GroupUses  []*resolve.HTTPUse // Uses selecting this group (incl. "default" for "")
}
```

  plus shared: `ServerUses []*resolve.HTTPUse` (no selector), `routeUses map[*resolve.HTTPRoute][]*resolve.HTTPUse`, `RespondFunc, WriteJSON, WriteError, WriteFailure, MaxBodyConst string`, `extractorField map[*resolve.HTTPExtractor]string` (App field via `e.varName[ex.Node.Key]`).
  - `planHTTP`: bucket plan routes by `Route.Group`; allocate names per group (default first, then sorted names); bucket Uses by selector kind; reserve the same idents plus nothing new (locals unchanged).
  - Constructor per group: reads flat cfg fields when `Name == ""`, else

```go
lc, ok := cfg.Groups["telemetry"]
if !ok {
	return nil, fmt.Errorf("http: group %q declared in the spec but missing from HTTPConfig.Groups", "telemetry")
}
```

  then per-route registration: routes with route-level Uses emit

```go
var h http.Handler = http.HandlerFunc(s.handleOrder)
h = a.audit.Middleware(h)              // reverse declaration order
mux.Handle("POST /order/{category}/", h)
```

  (no route Uses → `mux.HandleFunc(...)` exactly as today); after the mux:

```go
var handler http.Handler = mux
handler = a.auth.Middleware(handler)    // group-level, reverse declaration order
handler = a.recover.Middleware(handler) // server-level, reverse declaration order → outermost
```

  and `Handler: handler` in the `http.Server` literal (`Handler: mux` when no wraps).
  - `newFunc` hook (`httpSetup`): one block per group —

```go
httpServer, err := newHttpServer(a)
if err != nil { /* existing rollback pattern, then return nil, err */ }
a.httpServer = httpServer
```

  (reuse `writeConstructionRollback`-style unrolled rollback: all group servers are wired after Init, so the rollback is `a.Shutdown(ctx)`-free — mirror the Init-failure path: `report := a.Shutdown(ctx); return nil, errors.Join(err, report)`).
  - Adapters: dependency args come from `Args` — `arg.Node != nil` → `s.app.<field>`; extracted → emit before the handler call, with a per-adapter `NameAllocator` seeded with `w, r, req, q, raw, v, res, err, hs` for the local name (base `baseName(ex.ProducesType)`):

```go
user, err := s.app.userExtractor.Extract(r)
if err != nil {
	httpWriteFailure(w, "httpapp.Me2", "GET /me2", err)
	return
}
```

  - Replace the per-adapter error block with:

```go
if err != nil {
	var hs servo.HTTPStatus
	if errors.As(err, &hs) && hs.Code() < 300 {
		httpRespond(w, hs.Code(), res)
		return
	}
	httpWriteFailure(w, "httpapp.Order", "POST /order/{category}/", err)
	return
}
httpRespond(w, http.StatusOK, res)
```

  and emit once, package-level:

```go
func httpWriteFailure(w http.ResponseWriter, handler, route string, err error) {
	var hs servo.HTTPStatus
	if !errors.As(err, &hs) {
		slog.Error("servo: handler "+handler+" failed", "route", route, "error", err)
		httpWriteError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	code := hs.Code()
	if code >= 500 {
		slog.Error("servo: handler "+handler+" failed", "route", route, "status", code, "error", err)
		httpWriteError(w, code, hs.Error())
		return
	}
	httpWriteError(w, code, err.Error())
}
```

  (`httpRespond`, `httpWriteJSON`, `httpWriteError` are the old bodies without the receiver.)
  - `lifecycle.go`: `runFunc` counts every group server; `shutdownFunc` prepends one stop per group (default first, then sorted); `healthReadyFunc` Ready loop emits one entry per group, `"http"` / `"http:"+name`; stop names likewise.
  - `httpHeader`: add a `group: telemetry` suffix per route line and list declared groups.

- [ ] **Step 4: Run `go test ./internal/emit/`; refresh the golden deliberately** — `UPDATE_GOLDEN=1 go test ./internal/emit/ -run TestEmitHTTPGolden`, **review the golden diff by eye** (single-group fixture: expect the shared-helper refactor and `(T, error)` ctor; no group/middleware/extractor lines), re-run the full package. `fullapp.go.golden`/`scopedapp.go.golden` must be untouched.
- [ ] **Step 5: Commit** — `git commit -am "Emit one server per group with middleware chains and extractors"`

---

### Task 7: Pipeline — thread declarations, module-wide undeclared-group check, integration tests

**Files:**
- Modify: `cmd/servo/pipeline.go`, `cmd/servo/doctor.go`
- Test: `cmd/servo/http_test.go` (extend)

**Interfaces:**
- `pipeline.resolve` fills `HTTPInput.Groups/Uses/Extracts` from `p.spec.HTTP` and `p.spec.Extracts`.
- New helper consumed by `buildPipeline`, `buildPipelines`, `runDoctor`:

```go
// checkRouteGroups errors when a route names a group no spec declares —
// the typo that would otherwise silently leave the route unserved.
func checkRouteGroups(specs []*load.Spec, routes []*route.Route) error
```

- [ ] **Step 1: Write the failing tests** — extend `writeHTTPModule` acceptance: add to the fixture a `mw` package (Recover with ctx-value + response header, Auth returning 401 without `X-Token`, UserExtractor reading `X-User`), a telemetry-group healthz handler, `Groups`/`Use`/`Extract` in the spec, and a `Groups: map[string]servo.HTTPListener{"telemetry": {IP: "127.0.0.1"}}` entry in `NewHTTPConfig`. Assert generate → check → `go build` still pass and the generated file contains `httpTelemetryServer`. New negative tests:

```go
// A group token no spec declares fails generate at the directive.
func TestGenerateHTTPRejectsUndeclaredGroup(t *testing.T)
// want: `group "backoffice" is not declared by any injector` and the file:line

// A Use route selector matching nothing fails generate.
func TestGenerateHTTPRejectsUnmatchedUseRoute(t *testing.T)
// want: "matches no served route"

// Extracted type with a provider fails generate.
func TestGenerateHTTPRejectsExtractedAndProvided(t *testing.T)
// want: "never both"
```

- [ ] **Step 2: Run `go test ./cmd/servo/ -run TestGenerateHTTP`** — expected FAIL.

- [ ] **Step 3: Implement** — in `pipeline.go`: populate the new `HTTPInput` fields; add `checkRouteGroups` (union of every spec's `HTTP.Groups` names; every `rt.Group != ""` must be in the union, else one error listing each offender as `%s: servo: group %q is not declared by any injector — declare it with servo.Group(%q) inside servo.HTTP(...)`), called from `buildPipeline` and `buildPipelines` right after `FindSpec(s)`; in `doctor.go` call it too and keep the existing unserved-routes INFO line.

- [ ] **Step 4: Run `go test ./cmd/servo/`** — expected PASS.
- [ ] **Step 5: Commit** — `git commit -am "Wire groups, middleware and extractors through the pipeline"`

---

### Task 8: `examples/http` — groups, middleware, extractor, e2e

**Files:**
- Create: `examples/http/mw/mw.go`
- Modify: `examples/http/api/api.go`, `examples/http/cmd/app/spec.go`, `examples/http/cmd/app/app_test.go`, `examples/http/README.md`
- Regenerate: `examples/http/cmd/app/servo_gen.go`

**Interfaces:** consumes everything above; this is the end-to-end gate.

- [ ] **Step 1: Write the module changes and the failing e2e first.** `mw/mw.go`:

```go
// Package mw holds the example's middleware and extractor: a request-id
// middleware showing context mutation and a post-response header, an auth
// middleware guarding the internal group, and a user extractor feeding a
// typed handler parameter.
package mw

import (
	"context"
	"net/http"

	"github.com/okian/servo/v3/servo"
)

type ctxKey struct{}

type RequestID struct{}

func NewRequestID() *RequestID { return &RequestID{} }

func (m *RequestID) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = "generated-1"
		}
		w.Header().Set("X-Request-Id", id)                            // post-visible
		ctx := context.WithValue(r.Context(), ctxKey{}, id)           // context change
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// IDFromContext lets handlers and the e2e read what the middleware planted.
func IDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

type Auth struct{}

func NewAuth() *Auth { return &Auth{} }

func (m *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "letmein" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type User struct{ Name string }

type UserExtractor struct{}

func NewUserExtractor() *UserExtractor { return &UserExtractor{} }

func (e *UserExtractor) Extract(r *http.Request) (*User, error) {
	name := r.Header.Get("X-User")
	if name == "" {
		return nil, servo.Status.UNAUTHORIZED.New("missing X-User header")
	}
	return &User{Name: name}, nil
}
```

`api/api.go` additions: `HTTP_TELEMETRY_PORT` / `HTTP_INTERNAL_PORT` env handling filling `cfg.Groups["telemetry"]` / `cfg.Groups["internal"]`; new handlers —

```go
type HealthzResp struct {
	OK        bool   `json:"ok"`
	RequestID string `json:"request_id"`
}

//servo:get /healthz telemetry
func Healthz(ctx context.Context) (servo.Json[*HealthzResp], error) {
	return servo.JSON(&HealthzResp{OK: true, RequestID: mw.IDFromContext(ctx)}), nil
}

type ReplicateReq struct {
	Shard string `path:"shard"`
}
type ReplicateResp struct {
	Shard string `json:"shard"`
	By    string `json:"by"`
}

//servo:post /replicate/{shard} internal
func Replicate(ctx context.Context, req *ReplicateReq, user *mw.User) (servo.Json[*ReplicateResp], error) {
	return servo.JSON(&ReplicateResp{Shard: req.Shard, By: user.Name}), nil
}
```

Replace the old header-bound `Whoami` handler's request struct with the extractor (`func Whoami(ctx context.Context, user *mw.User) (servo.Json[*WhoamiResp], error)` — delete `WhoamiReq`; the 401 case now comes from the extractor). Spec:

```go
servo.Build(
	servo.HTTP(
		servo.Group("telemetry"),
		servo.Group("internal"),
		servo.Use[*mw.RequestID](),
		servo.Use[*mw.Auth](servo.Group("internal")),
	),
	servo.Extract[*mw.UserExtractor](),
)
```

e2e additions (`startApp` returns three base URLs from three free ports): healthz answers on the telemetry port and 404s on the public port; `X-Request-Id` header echoes on every response and `request_id` in the healthz body proves the ctx value reached the handler; internal route 401s without `X-Token` and works with it plus `X-User`; whoami 401s without `X-User` (extractor) and echoes with it; all prior subtests unchanged.

- [ ] **Step 2: Run — expected FAIL** (`servo_gen.go` stale / undefined). `cd /Users/kian/servo && go run ./cmd/servo generate --dir examples/http`, then `cd examples/http && go build ./... && go test -race ./...` until green; `golangci-lint run ./...`; `go run ./cmd/servo check --dir examples/http`.
- [ ] **Step 3: Commit** — `git add examples/http && git commit -m "Exercise groups, middleware and extractors in examples/http"`

---

### Task 9: Docs + CHANGELOG

**Files:**
- Modify: `docs/reference/http.md` (groups + middleware + extractors sections, "What v1 does not do" shrinks), `docs/reference/spec.md` (`HTTP` options, `Extract`, error table rows), `docs/reference/diagnostics.md` (new rows in the HTTP table), `docs/reference/servo-package.md` (`HTTPOption`, `Group`, `Use`, `Route`, `Extract`, `HTTPListener`, `HTTPConfig.Groups` — index + sections), `README.md` (HTTP section gains a groups/middleware/extractor paragraph + spec snippet), `ARCHITECTURE.md` (HTTP directives section: per-group servers, Use/Extract validation), `CHANGELOG.md` (extend the existing Unreleased HTTP bullet — same release, one story), `examples/http/README.md` already updated in Task 8.

- [ ] **Step 1: Write all doc updates.** Follow each page's existing register; every new diagnostic from Tasks 3–7 gets a row in `docs/reference/diagnostics.md`; the `servo-package.md` index gains one row per new identifier.
- [ ] **Step 2: Verify** — `typos`, and skim `docs/reference/http.md` against the spec: groups, config map, both middleware kinds, extractor failure semantics, wrap order, all present.
- [ ] **Step 3: Commit** — `git commit -am "Document HTTP groups, middleware and extractors"`

---

### Final verification (after Task 9)

- [ ] `go build ./... && go vet ./... && gofmt -l . && go test -race ./...` at the repo root.
- [ ] `go run ./cmd/servo check --dir examples/http` and every other example (`basic`, `scoped`, `mocking`, `variants` ± `--tags=prod`, `tutorial`) — all byte-identical.
- [ ] `golangci-lint run ./...` at the root and in `examples/http`.
- [ ] Read the refreshed `httpapp.go.golden` top to bottom once — it is the emitted-code contract.
