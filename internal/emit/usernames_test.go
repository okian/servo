package emit

import (
	"go/parser"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/load"
)

// addUserFile parses src as one more file of the injector package, the
// way the load carries a file whose declarations do not type-check yet —
// a hand-written method on the App that only the generated file declares.
func addUserFile(t *testing.T, spec *load.Spec, name, src string) {
	t.Helper()
	f, err := parser.ParseFile(spec.InjectorPkg.Fset, name, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	spec.InjectorPkg.Syntax = append(spec.InjectorPkg.Syntax, f)
}

// TestEmitStepsAroundTheInjectorPackagesNames: the package-level name
// table used to know only servo's own names, so a user's tracedStore or
// roomsAccessor in the injector package was declared a second time by the
// generated file, and the compile error was reported as a servo bug. Every
// name the emitters choose now steps around the package's own: here the
// scope's registry and accessor, and an import whose name a user variable
// already has.
func TestEmitStepsAroundTheInjectorPackagesNames(t *testing.T) {
	const depSrc = `
package dep

type Clock struct{}

func NewClock() *Clock { return &Clock{} }
`
	const appSrc = `
package app

import "context"

// Declared by the user, each spelled like something the generator writes.
type roomKeyScope struct{}
type roomsAccessor struct{}

var dep = "a variable named like the package"

var _, _, _ = roomKeyScope{}, roomsAccessor{}, dep

type RoomKey string

type Room struct{}

func NewRoom(k RoomKey) *Room { return &Room{} }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Server struct{}

func NewServer(r Rooms) *Server { return &Server{} }
`
	resolved, spec := buildFixture(t, fixtureSpec{
		roots:  []string{"Server", "Clock"},
		scopes: [][2]string{{"Room", "Rooms"}},
	}, fixturePkg{"example.com/dep", depSrc}, fixturePkg{"example.com/app", appSrc})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	gen := string(out)
	for _, want := range []string{
		"type roomKeyScope2 struct",
		"type roomsAccessor2 struct",
		`examplecomdep "example.com/dep"`,
		"examplecomdep.NewClock()",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, gen)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/dep", depSrc}, fixturePkg{"example.com/app", appSrc})
}

// TestEmitMovesTheTraceWrapperAroundAUserType is the same rule for the
// wrapper servo.Trace generates, tracedStore for a Store.
func TestEmitMovesTheTraceWrapperAroundAUserType(t *testing.T) {
	resolved, spec := buildTracedAppResolved(t)
	addUserFile(t, spec, "mine.go", "package app\n\ntype tracedStore struct{}\n")
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	for _, want := range []string{"type tracedStore2 struct", "func newTracedStore(inner Store, tp trace.TracerProvider) *tracedStore2 {"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("generated source missing %q\n---\n%s", want, out)
		}
	}
}

// TestEmitReportsNamesItCannotMove: the documented names — App, New, and
// the App's own methods — and the packages the emitters write by their
// own names cannot step aside, so a user declaration of one is reported
// at that declaration rather than left for the compiler to find in the
// generated file. A hand-written method that merely shares a field's name
// moves the field instead.
func TestEmitReportsNamesItCannotMove(t *testing.T) {
	const src = `
package app

import "context"

type App struct{}

var context2 = context.Background

type Logger struct{}

func NewLogger() *Logger { return &Logger{} }
func (*Logger) Stop(ctx context.Context) error { return nil }
`
	resolved, spec := buildResolvedFrom(t, src, []string{"Logger"}, nil)
	addUserFile(t, spec, "methods.go", `package app

import "context"

func (a *App) Run(ctx context.Context) error { return nil }
func (a *App) logger() {}

var os = 1
`)
	_, err := Emit(resolved, spec, false)
	if err == nil {
		t.Fatal("Emit accepted a package that declares App, App.Run and os")
	}
	for _, want := range []string{
		"app.go:6:6: App is declared in package app, and the generated file declares App, the type New returns, under that name",
		"methods.go:5:15: App.Run is declared in package app, and Run is part of the generated App's method set",
		"methods.go:8:5: os is declared in package app, and the generated file imports os under that name",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "logger") {
		t.Errorf("a method named like a field is something the field can step around, not an error:\n%v", err)
	}
}

// TestEmitMovesAFieldAroundAHandWrittenMethod: App's fields and methods
// share one namespace, and a method of the user's own on the generated App
// is ordinary Go, so the field gives way.
func TestEmitMovesAFieldAroundAHandWrittenMethod(t *testing.T) {
	const src = `
package app

type Logger struct{}

func NewLogger() *Logger { return &Logger{} }
`
	resolved, spec := buildResolvedFrom(t, src, []string{"Logger"}, nil)
	addUserFile(t, spec, "methods.go", "package app\n\nfunc (a *App) logger() *Logger { return a.logger2 }\n")
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if !strings.Contains(string(out), "logger2 := NewLogger()") {
		t.Errorf("the field did not step around the method:\n%s", out)
	}
}

// TestEmitSeesAMethodWhateverParenthesesItsReceiverHas: Go accepts a
// receiver type in parentheses, around the star or inside it, and the
// method is App's all the same. Read only as plainly spelled, such a
// method slipped past both checks above: a Run went unreported and the
// generated file declared App.Run a second time, and a field named like
// the method kept its name and clashed with it.
func TestEmitSeesAMethodWhateverParenthesesItsReceiverHas(t *testing.T) {
	const src = `
package app

type Logger struct{}

func NewLogger() *Logger { return &Logger{} }
`
	for _, recv := range []string{"(App)", "*(App)", "(*App)", "((*App))", "(*(App))"} {
		t.Run(recv, func(t *testing.T) {
			resolved, spec := buildResolvedFrom(t, src, []string{"Logger"}, nil)
			addUserFile(t, spec, "methods.go", "package app\n\nimport \"context\"\n\nfunc (a "+recv+") Run(ctx context.Context) error { return nil }\n")
			if _, err := Emit(resolved, spec, false); err == nil || !strings.Contains(err.Error(), "App.Run is declared in package app, and Run is part of the generated App's method set") {
				t.Errorf("Emit = %v, want App.Run reported", err)
			}

			resolved, spec = buildResolvedFrom(t, src, []string{"Logger"}, nil)
			addUserFile(t, spec, "methods.go", "package app\n\nfunc (a "+recv+") logger() *Logger { return a.logger2 }\n")
			out, err := Emit(resolved, spec, false)
			if err != nil {
				t.Fatalf("Emit: %v", err)
			}
			if !strings.Contains(string(out), "logger2 := NewLogger()") {
				t.Errorf("the field did not step around the method:\n%s", out)
			}
		})
	}
}

// TestEmitIgnoresTheSpecFilesDeclarations: a spec file is gated
// servoinject and never shares a build with the generated file, so what it
// declares — conventionally a func that holds servo.Build — collides with
// nothing.
func TestEmitIgnoresTheSpecFilesDeclarations(t *testing.T) {
	const src = `
package app

type Logger struct{}

func NewLogger() *Logger { return &Logger{} }
`
	resolved, spec := buildResolvedFrom(t, src, []string{"Logger"}, nil)
	addUserFile(t, spec, "spec.go", "//go:build servoinject\n\npackage app\n\ntype App struct{}\n\nfunc New() {}\n")
	if _, err := Emit(resolved, spec, false); err != nil {
		t.Fatalf("a spec file's declarations were held against the generated file: %v", err)
	}
}
