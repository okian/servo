package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeHTTPModule materializes a real module using the //servo: directive
// feature end to end: a handler with a request struct and a graph-resolved
// dependency, a dependency-only handler, a user-provided *servo.HTTPConfig,
// and a spec that opts in with servo.HTTP().
func writeHTTPModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)

	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module example.com/httpfix

go 1.26.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)
	write("repo/repo.go", `package repo

type Repository struct{}

func New() *Repository { return &Repository{} }

func (r *Repository) Open(category string) bool { return category != "closed" }
`)
	write("mw/mw.go", `package mw

import (
	"net/http"

	"github.com/okian/servo/v3/servo"
)

type RequestID struct{}

func NewRequestID() *RequestID { return &RequestID{} }

func (m *RequestID) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "fixed")
		next.ServeHTTP(w, r)
	})
}

type Auth struct{}

func NewAuth() *Auth { return &Auth{} }

func (m *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") == "" {
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
`)
	write("api/api.go", `package api

import (
	"context"

	"example.com/httpfix/mw"
	"example.com/httpfix/repo"
	"github.com/okian/servo/v3/servo"
)

func NewHTTPConfig() *servo.HTTPConfig {
	return &servo.HTTPConfig{
		IP: "127.0.0.1",
		Groups: map[string]servo.HTTPListener{
			"telemetry": {IP: "127.0.0.1"},
		},
	}
}

type HealthzResp struct {
	OK bool `+"`json:\"ok\"`"+`
}

//servo:get /healthz telemetry
func Healthz(ctx context.Context) (servo.Json[*HealthzResp], error) {
	return servo.JSON(&HealthzResp{OK: true}), nil
}

type WhoResp struct {
	Name string `+"`json:\"name\"`"+`
}

//servo:get /who
func Who(ctx context.Context, user *mw.User) (servo.Json[*WhoResp], error) {
	return servo.JSON(&WhoResp{Name: user.Name}), nil
}

type OrderReq struct {
	Category string `+"`path:\"category\"`"+`
	Page     int    `+"`query:\"page\"`"+`
	Note     string `+"`json:\"note\"`"+`
}

type OrderResp struct {
	Category string `+"`json:\"category\"`"+`
	Page     int    `+"`json:\"page\"`"+`
	Note     string `+"`json:\"note\"`"+`
}

//servo:post /order/{category}/
func Order(ctx context.Context, req *OrderReq, r *repo.Repository) (servo.Json[*OrderResp], error) {
	if !r.Open(req.Category) {
		return nil, servo.Status.FORBIDDEN.New("category is closed")
	}
	return servo.JSON(&OrderResp{Category: req.Category, Page: req.Page, Note: req.Note}), servo.Status.CREATED
}

//servo:get /ping
func Ping(ctx context.Context, r *repo.Repository) (servo.Json[*OrderResp], error) {
	return servo.JSON(&OrderResp{}), nil
}
`)
	write("cmd/app/main.go", `package main

func main() {}
`)
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/httpfix/mw"
	"github.com/okian/servo/v3/servo"
)

var _ = servo.Build(
	servo.HTTP(
		servo.Group("telemetry"),
		servo.Use[*mw.RequestID](),
		servo.Use[*mw.Auth](servo.Group("telemetry")),
	),
	servo.Extract[*mw.UserExtractor](),
)
`)
	runGoModTidy(t, dir)
	return dir
}

// TestGenerateHTTPModule is the end-to-end assertion that matters: the
// generated server compiles, and check agrees the file is fresh.
func TestGenerateHTTPModule(t *testing.T) {
	dir := writeHTTPModule(t)

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Fatalf("check after generate: %v", err)
	}

	gen, err := os.ReadFile(filepath.Join(dir, "cmd", "app", "servo_gen.go"))
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	for _, want := range []string{
		`mux.HandleFunc("POST /order/{category}/", s.handleOrder)`,
		`mux.HandleFunc("GET /ping", s.handlePing)`,
		"api.Order(r.Context(), req, s.app.repository)",
		"type httpTelemetryServer struct",
		"handler = a.requestID.Middleware(handler)",
		"user, err := s.app.userExtractor.Extract(r)",
	} {
		if !strings.Contains(string(gen), want) {
			t.Errorf("servo_gen.go missing %q", want)
		}
	}

	// Tidy as a real module does after its first generate: the fixture's
	// pre-generation go.sum holds only what its hand-written code needs.
	runGoModTidy(t, dir)

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated code does not compile: %v\n%s\n---\n%s", err, out, gen)
	}
}

// A typo under the reserved //servo: prefix must fail generate with a
// positioned diagnostic — never silently leave a route unserved.
func TestGenerateHTTPRejectsUnknownDirective(t *testing.T) {
	dir := writeHTTPModule(t)
	mustWriteFile(t, dir, "api/extra.go", `package api

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

//servo:pots /extra
func Extra(ctx context.Context) (servo.Json[*OrderResp], error) {
	return servo.JSON(&OrderResp{}), nil
}
`)

	err := runGenerate(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), `unknown //servo: directive "pots"`) {
		t.Fatalf("got err=%v, want the unknown-directive diagnostic", err)
	}
	if !strings.Contains(err.Error(), "api/extra.go") {
		t.Fatalf("diagnostic does not carry the file position: %v", err)
	}
}

// Declaring servo.HTTP() without providing *servo.HTTPConfig fails with the
// standard needed-by chain pointing at the marker.
func TestGenerateHTTPRequiresConfigProvider(t *testing.T) {
	dir := writeHTTPModule(t)
	// Remove the provider but keep the type usages compiling.
	mustWriteFile(t, dir, "api/api.go", `package api

import (
	"context"

	"example.com/httpfix/repo"
	"github.com/okian/servo/v3/servo"
)

type OrderResp struct {
	Category string `+"`json:\"category\"`"+`
}

//servo:get /ping
func Ping(ctx context.Context, r *repo.Repository) (servo.Json[*OrderResp], error) {
	return servo.JSON(&OrderResp{}), nil
}
`)

	err := runGenerate(cfg(dir))
	if err == nil {
		t.Fatalf("generate succeeded without a *servo.HTTPConfig provider")
	}
	for _, want := range []string{
		"no provider for *github.com/okian/servo/v3/servo.HTTPConfig",
		"needed by HTTP server (servo.HTTP())",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

// A group token no spec declares fails generate at the directive — the
// typo that would otherwise silently open no listener for the route.
func TestGenerateHTTPRejectsUndeclaredGroup(t *testing.T) {
	dir := writeHTTPModule(t)
	mustWriteFile(t, dir, "api/extra.go", `package api

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

//servo:get /extra backoffice
func Extra(ctx context.Context) (servo.Json[*HealthzResp], error) {
	return servo.JSON(&HealthzResp{}), nil
}
`)

	err := runGenerate(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), `group "backoffice" is not declared by any injector`) {
		t.Fatalf("got err=%v, want the undeclared-group diagnostic", err)
	}
	if !strings.Contains(err.Error(), "api/extra.go") {
		t.Fatalf("diagnostic does not carry the file position: %v", err)
	}
}

// A Use route selector matching nothing fails generate.
func TestGenerateHTTPRejectsUnmatchedUseRoute(t *testing.T) {
	dir := writeHTTPModule(t)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/httpfix/mw"
	"github.com/okian/servo/v3/servo"
)

var _ = servo.Build(
	servo.HTTP(
		servo.Group("telemetry"),
		servo.Use[*mw.RequestID](servo.Route("POST /missing")),
	),
	servo.Extract[*mw.UserExtractor](),
)
`)

	err := runGenerate(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "matches no served route") {
		t.Fatalf("got err=%v, want the unmatched-selector diagnostic", err)
	}
}

// An extracted type with its own provider fails generate: a type is
// constructed once or extracted per request, never both.
func TestGenerateHTTPRejectsExtractedAndProvided(t *testing.T) {
	dir := writeHTTPModule(t)
	mustWriteFile(t, dir, "mw/provider.go", `package mw

func NewUser() *User { return &User{} }
`)

	err := runGenerate(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "never both") {
		t.Fatalf("got err=%v, want the extracted-and-provided diagnostic", err)
	}
}

// servo list answers "what does servo see" — with directives in the module,
// that must include the routes, not only the constructors.
func TestListShowsRoutes(t *testing.T) {
	dir := writeHTTPModule(t)
	out := captureStdout(t, func() {
		if err := runList(cfg(dir), false, false, false); err != nil {
			t.Errorf("runList: %v", err)
		}
	})
	for _, want := range []string{
		"routes:",
		"POST /order/{category}/",
		"api.Order",
		"GET /healthz [telemetry]",
		"api.Healthz",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

// TestGenerateHTTPHandlerInAnotherPackageMain: a directive on a function in
// a sibling binary's main package used to be served by every HTTP injector
// in the module, which meant importing a main package — `servo generate`
// exited 0 and `go build` failed with "is a program, not an importable
// package". Now the owning package has to serve it: with no injector there
// (or one without servo.HTTP()) generate says so at the directive, and once
// that package declares servo.HTTP() each injector serves exactly its own.
func TestGenerateHTTPHandlerInAnotherPackageMain(t *testing.T) {
	dir := writeHTTPModule(t)
	mustWriteFile(t, dir, "cmd/other/main.go", `package main

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type PingResp struct {
	OK bool `+"`json:\"ok\"`"+`
}

//servo:get /ping-from-main
func PingFromMain(ctx context.Context) (servo.Json[*PingResp], error) {
	return servo.JSON(&PingResp{OK: true}), nil
}

func main() {}
`)

	err := runGenerate(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "declared in package main (example.com/httpfix/cmd/other)") || !strings.Contains(err.Error(), "has no servo.Build(...)") {
		t.Fatalf("got err=%v, want the package-main diagnostic naming the owning package", err)
	}
	if !strings.Contains(err.Error(), "cmd/other/main.go") {
		t.Fatalf("diagnostic does not carry the directive's position: %v", err)
	}

	// An injector in that package without servo.HTTP() is the other half
	// of the same mistake.
	mustWriteFile(t, dir, "cmd/other/spec.go", `//go:build servoinject

package main

import "github.com/okian/servo/v3/servo"

var _ = servo.Build()
`)
	err = runGenerate(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "injector declares no servo.HTTP()") {
		t.Fatalf("got err=%v, want the no-servo.HTTP() diagnostic", err)
	}

	// With servo.HTTP() declared there, each injector serves its own
	// main-package handlers and skips the other's — and both compile.
	mustWriteFile(t, dir, "cmd/other/spec.go", `//go:build servoinject

package main

import (
	"example.com/httpfix/mw"
	"github.com/okian/servo/v3/servo"
)

// Every module route lands here too, so the extractor api.Who needs is
// declared; the telemetry group is not, so api.Healthz is skipped.
var _ = servo.Build(
	servo.HTTP(),
	servo.Extract[*mw.UserExtractor](),
)
`)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate with both injectors serving: %v", err)
	}
	appGen := mustRead(t, filepath.Join(dir, "cmd", "app", "servo_gen.go"))
	if strings.Contains(appGen, "ping-from-main") || strings.Contains(appGen, "example.com/httpfix/cmd/other") {
		t.Fatalf("cmd/app's server still reaches into cmd/other's main package:\n%s", appGen)
	}
	otherGen := mustRead(t, filepath.Join(dir, "cmd", "other", "servo_gen.go"))
	if !strings.Contains(otherGen, `mux.HandleFunc("GET /ping-from-main", s.handlePingFromMain)`) || !strings.Contains(otherGen, "PingFromMain(r.Context())") {
		t.Fatalf("cmd/other's own injector does not serve its handler:\n%s", otherGen)
	}
	runGoModTidy(t, dir)
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated code does not compile: %v\n%s", err, out)
	}
}

// TestWhyAndExplainAttributeHTTPDependencies: a node only a handler,
// middleware attachment or extractor pulls in descends from no
// servo.Root, and `why` used to call it unreachable while `explain`
// showed no dependents. The resolver knows exactly which declaration
// asked for it, and now both commands say so.
func TestWhyAndExplainAttributeHTTPDependencies(t *testing.T) {
	dir := writeHTTPModule(t)

	out := captureStdout(t, func() {
		if err := runWhy(cfg(dir), "repo.Repository", false); err != nil {
			t.Fatalf("runWhy: %v", err)
		}
	})
	if !strings.Contains(out, "root  handler api.Order, POST /order/{category}/") || !strings.Contains(out, "-> *example.com/httpfix/repo.Repository") {
		t.Errorf("why does not name the handler as the entry point:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runWhy(cfg(dir), "repo.Repository", true); err != nil {
			t.Fatalf("runWhy --json: %v", err)
		}
	})
	if !strings.Contains(out, `"handler api.Order, POST /order/{category}/"`) {
		t.Errorf("why --json does not carry the entry point:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runExplain(cfg(dir), "repo.Repository", false); err != nil {
			t.Fatalf("runExplain: %v", err)
		}
	})
	for _, want := range []string{"(handler api.Order, POST /order/{category}/)", "(handler api.Ping, GET /ping)"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q:\n%s", want, out)
		}
	}

	// A middleware and an extractor are named by their declarations.
	out = captureStdout(t, func() {
		if err := runExplain(cfg(dir), "mw.Auth", false); err != nil {
			t.Fatalf("runExplain: %v", err)
		}
	})
	if !strings.Contains(out, "(servo.Use[*example.com/httpfix/mw.Auth], group telemetry)") {
		t.Errorf("explain output does not name the Use attachment:\n%s", out)
	}
	out = captureStdout(t, func() {
		if err := runWhy(cfg(dir), "mw.UserExtractor", false); err != nil {
			t.Fatalf("runWhy: %v", err)
		}
	})
	if !strings.Contains(out, "root  servo.Extract[*example.com/httpfix/mw.UserExtractor]") {
		t.Errorf("why does not name the Extract declaration:\n%s", out)
	}
}
