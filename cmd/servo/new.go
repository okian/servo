package main

import (
	"fmt"
	"go/token"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/okian/servo/v3/internal/graph"
)

// The three shapes `servo new` accepts, each naming the flags it takes:
// --package means nothing to an adapter, whose argument is its package.
// The command table builds the usage line from them and runNew's own
// errors quote them, so the two cannot describe different arguments.
const (
	newComponentForm   = "component [--package name] <Name>"
	newAdapterForm     = "adapter <pkgname>"
	newMockAdapterForm = "mock-adapter [--package name] <moq|mockery|gomock> <GeneratedTypeName>"
)

// defaultMockPackage is a mock adapter's package when none is named. The
// adapter has to sit beside the generated mock, in its package, and mocks
// is mockery's default output package and the tutorial's for mockgen;
// moq and mockgen users who chose another pass it with --package.
const defaultMockPackage = "mocks"

// runNew scaffolds a component, adapter, or mock adapter: a complete Go
// file printed to stdout — package clause, imports and all — that never
// imports servo, so the caller decides where it lands. Complete means it
// compiles as printed and what it declares resolves under `servo
// generate`; a scaffold needing a fix before its first generation teaches
// the wrong lesson at exactly the moment someone is learning the tool.
func runNew(kind string, args []string, pkg string) error {
	switch kind {
	case "component":
		if len(args) != 1 {
			return fmt.Errorf("usage: servo new %s", newComponentForm)
		}
		name := args[0]
		if err := checkExportedIdent(kind, "type name", name); err != nil {
			return err
		}
		if pkg == "" {
			pkg = strings.ToLower(name)
		}
		if err := checkPackageName(kind, pkg); err != nil {
			return err
		}
		printComponentScaffold(pkg, name)
		return nil
	case "adapter":
		if len(args) != 1 {
			return fmt.Errorf("usage: servo new %s", newAdapterForm)
		}
		if pkg != "" {
			return fmt.Errorf("servo new adapter: --package does not apply — the argument is the package (usage: servo new %s)", newAdapterForm)
		}
		pkg = args[0]
		if err := checkPackageName(kind, pkg); err != nil {
			return err
		}
		// The package names the environment variables too, through the
		// config's prefix, so it has to make one //servo:config accepts —
		// checked with the scanner's own rule rather than a copy of it.
		prefix := strings.ToUpper(strings.TrimLeft(pkg, "_"))
		if _, _, err := graph.ParseConfigDirectiveOptions([]string{"prefix=" + prefix}); err != nil {
			return fmt.Errorf("servo new adapter: package %q would give its config the prefix %s, which servo refuses: %w", pkg, prefix, err)
		}
		printAdapterScaffold(pkg, prefix)
		return nil
	case "mock-adapter":
		if len(args) != 2 {
			return fmt.Errorf("usage: servo new %s", newMockAdapterForm)
		}
		tool, typeName := args[0], args[1]
		if err := checkExportedIdent(kind, "type name", typeName); err != nil {
			return err
		}
		if pkg == "" {
			pkg = defaultMockPackage
		}
		if err := checkPackageName(kind, pkg); err != nil {
			return err
		}
		return printMockAdapterScaffold(tool, typeName, pkg)
	default:
		return fmt.Errorf("servo new: unknown kind %q (want component, adapter or mock-adapter)\nusage: servo new %s\n       servo new %s\n       servo new %s",
			kind, newComponentForm, newAdapterForm, newMockAdapterForm)
	}
}

// checkExportedIdent refuses a type name the scaffold would print into Go
// source that cannot compile there, or that a spec file in another
// package could not name.
func checkExportedIdent(kind, what, name string) error {
	if !token.IsIdentifier(name) {
		return fmt.Errorf("servo new %s: %s %q is not a Go identifier", kind, what, name)
	}
	if r, _ := utf8.DecodeRuneInString(name); !unicode.IsUpper(r) {
		return fmt.Errorf("servo new %s: %s %q is unexported — the spec file that names it lives in another package, so start it with an upper-case letter", kind, what, name)
	}
	return nil
}

// checkPackageName refuses a package clause that would not compile.
func checkPackageName(kind, pkg string) error {
	if !token.IsIdentifier(pkg) || pkg == "_" {
		return fmt.Errorf("servo new %s: package %q is not a valid package name", kind, pkg)
	}
	return nil
}

// printComponentScaffold leaves every capability commented out, so the
// file compiles as printed and adds nothing to the lifecycle until one is
// chosen — which is also why it imports nothing: a context import with no
// method using it would not compile.
func printComponentScaffold(pkg, name string) {
	fmt.Printf(`package %[1]s

// %[2]s is a component: servo constructs it with New%[2]s and calls
// whichever lifecycle methods below it has.
type %[2]s struct{}

// New%[2]s is %[2]s's provider. Its parameters are %[2]s's dependencies:
// servo generate finds the constructor by its signature and passes them
// in, with no registration.
func New%[2]s() *%[2]s {
	return &%[2]s{}
}

// Uncomment whichever capabilities %[2]s needs — servo generate detects
// them structurally — and import "context" along with the first one.
//
// func (c *%[2]s) Init(ctx context.Context) error   { return nil }
// func (c *%[2]s) Run(ctx context.Context) error    { <-ctx.Done(); return nil }
// func (c *%[2]s) Drain(ctx context.Context) error  { return nil }
// func (c *%[2]s) Flush(ctx context.Context) error  { return nil }
// func (c *%[2]s) Stop(ctx context.Context) error   { return nil }
// func (c *%[2]s) Health(ctx context.Context) error { return nil }
// func (c *%[2]s) Ready(ctx context.Context) error  { return nil }
`, pkg, name)
}

// printAdapterScaffold is the wrapper a third-party client needs before
// the graph can depend on it: a type the module owns, a constructor servo
// can call, and the client's settings as a //servo:config type, so
// generation writes their loader instead of failing on a config nothing
// provides. Teardown is Stop alone — a context, and an error the shutdown
// report records — rather than a cleanup func beside it, which would be a
// second way to release the same connection.
func printAdapterScaffold(pkg, prefix string) {
	fmt.Printf(`package %[1]s

import (
	"context"
	"time"
)

// config is what Client needs to reach the service. servo generate writes
// its loader into this package: each tagged field is read from the
// environment variable %[2]s_<NAME> — %[2]s_ADDR, %[2]s_TIMEOUT — and a
// deployment missing a required one fails at startup, naming it. It can
// stay unexported because the generated loader lives here too.
//
//servo:config prefix=%[2]s
type config struct {
	addr    string        `+"`config:\"addr,required\"`"+`
	timeout time.Duration `+"`config:\"timeout,default=5s\"`"+`
}

// Client wraps the third-party client, so the graph depends on a type
// this module owns.
type Client struct {
	cfg config
}

// New is Client's provider. Keep it free of I/O: connecting belongs in
// Init, which gets a context and a place in the startup order.
func New(cfg config) (*Client, error) {
	return &Client{cfg: cfg}, nil
}

// Init connects, within c.cfg.timeout. servo calls it once, before
// anything that depends on Client starts.
func (c *Client) Init(ctx context.Context) error {
	return nil
}

// Stop releases the connection. servo calls it during Shutdown, after
// everything that depends on Client has stopped.
func (c *Client) Stop(ctx context.Context) error {
	return nil
}

// Health reports whether the service is reachable.
func (c *Client) Health(ctx context.Context) error {
	return nil
}
`, pkg, prefix)
}

// printMockAdapterScaffold prints the adapter file every mock integration
// needs: none of moq, mockery, or gomock generate a provider shape servo
// can call directly (see the README's Mocking section for why), and
// rediscovering the fix for each tool independently is exactly the
// friction this scaffold exists to remove. The file goes beside the
// generated mock, in its package, since it uses the mock's own names.
//
// mockgen is accepted for gomock: it is the name of the binary, and of
// the go:generate line, that people actually run.
func printMockAdapterScaffold(tool, typeName, pkg string) error {
	switch tool {
	case "moq":
		printMoqAdapterScaffold(typeName, pkg)
	case "mockery":
		printMockeryAdapterScaffold(typeName, pkg)
	case "gomock", "mockgen":
		printGomockAdapterScaffold(typeName, pkg)
	default:
		return fmt.Errorf("servo new mock-adapter: unknown tool %q (want moq, mockery or gomock — mockgen is accepted for gomock)", tool)
	}
	return nil
}

// printMoqAdapterScaffold covers moq: it generates a plain struct with
// function fields and no constructor at all, so the fix is just adding one.
func printMoqAdapterScaffold(typeName, pkg string) {
	fmt.Printf(`package %[2]s

// New%[1]s gives moq's %[1]s the constructor moq does not generate:
// servo builds a graph by calling provider functions, never by evaluating
// a composite literal. Keep it in this separate file — regenerating the
// mock would erase anything added to the generated one.
func New%[1]s() *%[1]s { return &%[1]s{} }
`, typeName, pkg)
}

// printMockeryAdapterScaffold covers mockery: its generated constructor
// takes a *testing.T-shaped value to auto-register AssertExpectations,
// which is itself a valid provider shape servo could call — making a
// second, zero-arg constructor for the same type a genuine ambiguity. The
// fix is a distinct wrapper type, not a second constructor.
func printMockeryAdapterScaffold(typeName, pkg string) {
	fmt.Printf(`package %[2]s

// %[1]sMock embeds mockery's generated *%[1]s — a distinct result type,
// so it never collides with %[1]s's own constructor.
type %[1]sMock struct{ *%[1]s }

func New%[1]sMock() *%[1]sMock { return &%[1]sMock{%[1]s: &%[1]s{}} }
`, typeName, pkg)
}

// printGomockAdapterScaffold covers gomock: same wrapping requirement as
// mockery, for the same reason, plus *gomock.Controller itself needing a
// TestReporter with no *testing.T reachable from inside the graph —
// servotest.PanicReporter supplies one without adding a gomock dependency
// to servotest itself.
func printGomockAdapterScaffold(typeName, pkg string) {
	fmt.Printf(`package %[2]s

import (
	"github.com/okian/servo/v3/servotest"
	"go.uber.org/mock/gomock"
)

// %[1]sForServo wraps gomock-generated *%[1]s, with a controller whose
// reporter panics: there is no *testing.T inside the graph to report to.
type %[1]sForServo struct {
	*%[1]s
	Finish func()
}

func New%[1]sForServo() *%[1]sForServo {
	ctrl := gomock.NewController(servotest.PanicReporter{})
	return &%[1]sForServo{%[1]s: New%[1]s(ctrl), Finish: ctrl.Finish}
}

// Finish must be called directly by the test (a plain defer), right after
// construction — never routed through servo's (T, func()) cleanup shape.
// That would run it inside the stop goroutine (servoimpl.RunStop) during Shutdown,
// where an unmet expectation's panic is recovered into the shutdown
// report, and a test that never reads the report passes.
`, typeName, pkg)
}
