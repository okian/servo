package emit

import (
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
)

// TestEmitConfigLoaderWithNothingRequired: a config whose every setting
// is optional has nothing to report missing, so its loader collects
// nothing and imports nothing to report it with; and a string default is
// written as a quoted Go literal, whatever characters it holds.
func TestEmitConfigLoaderWithNothingRequired(t *testing.T) {
	const structSrc = `
package app

type settings struct {
	dsn     string
	verbose bool
}

var _ = settings{}.dsn
`
	const dsn = `postgres://localhost/app?sslmode=disable&application_name="servo"`
	for _, withFile := range []bool{false, true} {
		decl := loaderDecl()
		decl.Fields = []graph.ConfigField{
			{FieldName: "dsn", Name: "dsn", EnvName: "APP_DSN", FileKey: "dsn", Kind: graph.KindString, HasDefault: true, Default: dsn},
			{FieldName: "verbose", Name: "verbose", EnvName: "APP_VERBOSE", FileKey: "verbose", Kind: graph.KindBool},
		}
		out, err := EmitConfigLoader(decl, withFile)
		if err != nil {
			t.Fatalf("EmitConfigLoader(withFile=%v): %v", withFile, err)
		}
		src := string(out)
		if want := "cfg.dsn = " + `"postgres://localhost/app?sslmode=disable&application_name=\"servo\""`; !strings.Contains(src, want) {
			t.Errorf("withFile=%v: loader missing %q\n---\n%s", withFile, want, src)
		}
		for _, unwanted := range []string{"missing", `"strings"`} {
			if strings.Contains(src, unwanted) {
				t.Errorf("withFile=%v: a loader with nothing required still has %s\n---\n%s", withFile, unwanted, src)
			}
		}
		typecheckGenerated(t, out, fixturePkg{"example.com/app", structSrc})
	}
}

// TestEmitConfigLoaderReturnsErrorWhenItFailsToFormat covers the loader's
// format.Source failure the way TestEmitReturnsErrorWhenGeneratedSource-
// FailsToFormat covers Emit's: nothing scan produces is invalid Go, so the
// branch is reached with the raw package name a servo bug would feed it,
// and has to come back as an error naming servo, not as a panic or a
// broken file.
func TestEmitConfigLoaderReturnsErrorWhenItFailsToFormat(t *testing.T) {
	decl := loaderDecl()
	decl.PkgName = "type" // a keyword, illegal as a package name
	_, err := EmitConfigLoader(decl, false)
	if err == nil || !strings.Contains(err.Error(), "generated config loader failed to format (this is a servo bug, not a user error)") {
		t.Fatalf("got err=%v, want the loader's 'failed to format' error", err)
	}
}

// TestEmitCallsAConfigLoaderInAnotherPackage: a //servo:config type's
// loader is generated into the type's own package, so an injector
// elsewhere calls it qualified, through an import — in New and in
// ValidateConfig alike.
func TestEmitCallsAConfigLoaderInAnotherPackage(t *testing.T) {
	const dbconfSrc = `
package dbconf

//servo:config prefix=DB
type Settings struct {
	DSN string ` + "`config:\"dsn,required\"`" + `
}
`
	const appSrc = `
package app

import "example.com/dbconf"

type Server struct{}

func NewServer(s dbconf.Settings) *Server { return &Server{} }
`
	resolved, spec := buildFixture(t, fixtureSpec{roots: []string{"Server"}, configs: true},
		fixturePkg{"example.com/dbconf", dbconfSrc}, fixturePkg{"example.com/app", appSrc})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	for _, want := range []string{
		`"example.com/dbconf"`,
		"settings, settingsErr := dbconf.ServoConfig()",
		"func ValidateConfig() error {\n\t_, settingsErr := dbconf.ServoConfig()\n",
		"server := NewServer(settings)",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("generated source missing %q\n---\n%s", want, out)
		}
	}
	// The load never sees the loader servo generate writes beside
	// Settings, since it would be a second constructor for it; the
	// compiler does, so a stub with its signature stands in for it here.
	loader := "\nfunc ServoConfig() (Settings, error) { return Settings{}, nil }\n"
	typecheckGenerated(t, out, fixturePkg{"example.com/dbconf", dbconfSrc + loader}, fixturePkg{"example.com/app", appSrc})
}

// TestEmitWritesTheConstraintLoadComputed: a graph resolved under build
// tags may compile only under them, and load works out the constraint
// from the spec file's own; the emitted file carries it verbatim in place
// of the plain mirror image of the spec's tag.
func TestEmitWritesTheConstraintLoadComputed(t *testing.T) {
	const src = `
package app

type Leaf struct{}

func NewLeaf() *Leaf { return &Leaf{} }
`
	resolved, spec := buildResolvedFrom(t, src, []string{"Leaf"}, nil)
	spec.GeneratedConstraint = "!servoinject && prod"
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if !strings.HasPrefix(string(out), "//go:build !servoinject && prod\n") {
		t.Errorf("the file does not open with the computed constraint:\n%s", out)
	}
}

// TestEmitHandsConstructorsTheConstructionContext: a constructor taking a
// context.Context is given the one New was called with, New's own ctx
// parameter, rather than a node of its own or a context made up for it.
func TestEmitHandsConstructorsTheConstructionContext(t *testing.T) {
	const src = `
package app

import "context"

type DB struct{}

func NewDB(ctx context.Context) (*DB, error) { return &DB{}, ctx.Err() }

type Server struct{}

func NewServer(ctx context.Context, db *DB) *Server { return &Server{} }
`
	resolved, spec := buildResolvedFrom(t, src, []string{"Server"}, nil)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	for _, want := range []string{"db, err := NewDB(ctx)", "server := NewServer(ctx, db)"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("generated source missing %q\n---\n%s", want, out)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", src})
}
