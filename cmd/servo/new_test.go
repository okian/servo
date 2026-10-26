package main

import (
	"go/format"
	"os/exec"
	"strings"
	"testing"
)

// scaffold runs `servo new` in process and returns what it printed.
func scaffold(t *testing.T, kind string, args []string, pkg string) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runNew(kind, args, pkg) })
	if err != nil {
		t.Fatalf("runNew(%s, %v, %q): %v", kind, args, pkg, err)
	}
	// A scaffold is pasted into a file and committed; one gofmt would
	// rewrite is a diff in the user's first commit that servo put there.
	formatted, err := format.Source([]byte(out))
	if err != nil {
		t.Fatalf("scaffold does not parse: %v\n%s", err, out)
	}
	if string(formatted) != out {
		t.Errorf("scaffold is not gofmt-clean; gofmt gives:\n%s\nprinted:\n%s", formatted, out)
	}
	return out
}

func TestRunNewComponent(t *testing.T) {
	out := scaffold(t, "component", []string{"Widget"}, "")
	for _, want := range []string{
		// The package defaults to the type name, lowercased: one component
		// per package is the layout the scaffold assumes.
		"package widget\n",
		"type Widget struct{}",
		"func NewWidget() *Widget {",
		"return &Widget{}",
		"func (c *Widget) Init(ctx context.Context) error",
		"func (c *Widget) Ready(ctx context.Context) error",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("component scaffold missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\nimport ") {
		t.Errorf("every capability is commented out, so an import would not compile:\n%s", out)
	}
}

func TestRunNewComponentTakesAPackage(t *testing.T) {
	out := scaffold(t, "component", []string{"Widget"}, "parts")
	if !strings.HasPrefix(out, "package parts\n") {
		t.Errorf("--package parts did not reach the package clause:\n%s", out)
	}
}

// TestRunNewAdapter pins the two defects the old scaffold had: a Config
// nothing provided, so rooting the adapter failed with "no provider for
// redis.Config", and a cleanup func beside Stop — two ways to release one
// connection.
func TestRunNewAdapter(t *testing.T) {
	out := scaffold(t, "adapter", []string{"redis"}, "")
	for _, want := range []string{
		"package redis\n",
		"//servo:config prefix=REDIS\ntype config struct {",
		"`config:\"addr,required\"`",
		"func New(cfg config) (*Client, error)",
		"func (c *Client) Init(ctx context.Context) error",
		"func (c *Client) Stop(ctx context.Context) error",
		"func (c *Client) Health(ctx context.Context) error",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("adapter scaffold missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "func()") {
		t.Errorf("the adapter should tear down through Stop alone, not a cleanup func as well:\n%s", out)
	}
}

func TestRunNewAdapterRefusesWhatItCannotPrint(t *testing.T) {
	for _, c := range []struct {
		args []string
		pkg  string
		want string
	}{
		{[]string{"my-redis"}, "", "not a valid package name"},
		{[]string{"func"}, "", "not a valid package name"},
		{[]string{"redis"}, "other", "--package does not apply"},
	} {
		err := runNew("adapter", c.args, c.pkg)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("runNew(adapter, %v, %q) = %v, want an error containing %q", c.args, c.pkg, err, c.want)
		}
	}
}

func TestRunNewMockAdapterMoq(t *testing.T) {
	out := scaffold(t, "mock-adapter", []string{"moq", "StoreMock"}, "")
	for _, want := range []string{
		"package mocks\n",
		"func NewStoreMock() *StoreMock { return &StoreMock{} }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("moq mock-adapter scaffold missing %q, got:\n%s", want, out)
		}
	}
}

func TestRunNewMockAdapterMockery(t *testing.T) {
	out := scaffold(t, "mock-adapter", []string{"mockery", "Store"}, "")
	for _, want := range []string{
		"type StoreMock struct{ *Store }",
		"func NewStoreMock() *StoreMock { return &StoreMock{Store: &Store{}} }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mockery mock-adapter scaffold missing %q, got:\n%s", want, out)
		}
	}
}

func TestRunNewMockAdapterGomock(t *testing.T) {
	out := scaffold(t, "mock-adapter", []string{"gomock", "MockStore"}, "")
	for _, want := range []string{
		`"go.uber.org/mock/gomock"`,
		`"github.com/okian/servo/v3/servotest"`,
		"type MockStoreForServo struct {",
		"*MockStore",
		"Finish func()",
		"func NewMockStoreForServo() *MockStoreForServo {",
		"gomock.NewController(servotest.PanicReporter{})",
		"MockStore: NewMockStore(ctrl), Finish: ctrl.Finish",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("gomock mock-adapter scaffold missing %q, got:\n%s", want, out)
		}
	}
}

// TestRunNewMockAdapterAcceptsMockgen: mockgen is the binary gomock users
// run and the word in their go:generate line, so it is what they type.
func TestRunNewMockAdapterAcceptsMockgen(t *testing.T) {
	gomock := scaffold(t, "mock-adapter", []string{"gomock", "MockStore"}, "")
	mockgen := scaffold(t, "mock-adapter", []string{"mockgen", "MockStore"}, "")
	if gomock != mockgen {
		t.Errorf("mockgen should print gomock's scaffold:\n--- gomock\n%s\n--- mockgen\n%s", gomock, mockgen)
	}
}

func TestRunNewMockAdapterUnknownTool(t *testing.T) {
	err := runNew("mock-adapter", []string{"bogus", "Store"}, "")
	if err == nil || !strings.Contains(err.Error(), "unknown tool") || !strings.Contains(err.Error(), "mockgen") {
		t.Fatalf("got err=%v, want an 'unknown tool' error that lists the accepted names", err)
	}
}

func TestRunNewMockAdapterWrongArgCount(t *testing.T) {
	for _, args := range [][]string{nil, {"moq"}, {"moq", "Store", "extra"}} {
		if err := runNew("mock-adapter", args, ""); err == nil || !strings.Contains(err.Error(), "usage: servo new mock-adapter") {
			t.Errorf("runNew(mock-adapter, %v): got %v, want a usage error", args, err)
		}
	}
}

func TestRunNewUnknownKind(t *testing.T) {
	err := runNew("bogus", []string{"x"}, "")
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("got err=%v, want an 'unknown kind' error", err)
	}
}

func TestRunNewComponentWrongArgCount(t *testing.T) {
	if err := runNew("component", nil, ""); err == nil || !strings.Contains(err.Error(), "usage: servo new component") {
		t.Errorf("runNew(component, no args): got %v, want a usage error", err)
	}
	if err := runNew("component", []string{"A", "B"}, ""); err == nil || !strings.Contains(err.Error(), "usage: servo new component") {
		t.Errorf("runNew(component, 2 args): got %v, want a usage error", err)
	}
}

func TestRunNewAdapterWrongArgCount(t *testing.T) {
	if err := runNew("adapter", nil, ""); err == nil || !strings.Contains(err.Error(), "usage: servo new adapter") {
		t.Errorf("runNew(adapter, no args): got %v, want a usage error", err)
	}
}

// TestRunNewRefusesNamesASpecCannotUse: the old scaffold printed whatever
// it was given — `servo new component --help` produced `type --help
// struct{}` — and an unexported type compiles but can never be named by
// the spec file in another package.
func TestRunNewRefusesNamesASpecCannotUse(t *testing.T) {
	for _, c := range []struct {
		kind string
		args []string
		pkg  string
		want string
	}{
		{"component", []string{"--help"}, "", "not a Go identifier"},
		{"component", []string{"widget"}, "", "unexported"},
		{"component", []string{"Widget"}, "my-pkg", "not a valid package name"},
		{"mock-adapter", []string{"moq", "storeMock"}, "", "unexported"},
	} {
		err := runNew(c.kind, c.args, c.pkg)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("runNew(%s, %v, %q) = %v, want an error containing %q", c.kind, c.args, c.pkg, err, c.want)
		}
	}
}

// TestNewScaffoldsCompileAndResolve is the claim the scaffolds make, made
// against the toolchain: every one of them, written as printed into a
// module beside stand-ins for the generated mocks, is rooted in one spec,
// generated, built and vetted. gomock comes from a local stub module, so
// the test needs no network; the scaffold only touches NewController and
// Finish, whose shape the stub copies.
func TestNewScaffoldsCompileAndResolve(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module example.com/scaffolds

go 1.23

require (
	github.com/okian/servo/v3 v3.0.0
	go.uber.org/mock v0.6.0
)

replace github.com/okian/servo/v3 => `+repoRoot(t)+`

replace go.uber.org/mock => ./stub/mock
`)
	write("stub/mock/go.mod", "module go.uber.org/mock\n\ngo 1.23\n")
	write("stub/mock/gomock/gomock.go", `package gomock

type TestReporter interface {
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

type ControllerOption interface{ apply(*Controller) }

type Controller struct{ T TestReporter }

func NewController(t TestReporter, opts ...ControllerOption) *Controller { return &Controller{T: t} }

func (c *Controller) Finish() {}
`)

	write("widget/widget.go", scaffold(t, "component", []string{"Widget"}, ""))
	write("redisx/redisx.go", scaffold(t, "adapter", []string{"redisx"}, ""))

	// What moq, mockery and mockgen generate, reduced to the shapes the
	// adapters depend on.
	write("moqmocks/store_mock.go", `package moqmocks

type StoreMock struct{ GetFunc func(key string) string }

func (m *StoreMock) Get(key string) string { return m.GetFunc(key) }
`)
	write("moqmocks/adapter.go", scaffold(t, "mock-adapter", []string{"moq", "StoreMock"}, "moqmocks"))
	write("mockerymocks/store.go", `package mockerymocks

type Store struct{}

func NewStore(t interface{ Cleanup(func()) }) *Store { return &Store{} }

func (s *Store) Get(key string) string { return "" }
`)
	write("mockerymocks/adapter.go", scaffold(t, "mock-adapter", []string{"mockery", "Store"}, "mockerymocks"))
	write("gomockmocks/store_mock.go", `package gomockmocks

import "go.uber.org/mock/gomock"

type MockStore struct{ ctrl *gomock.Controller }

func NewMockStore(ctrl *gomock.Controller) *MockStore { return &MockStore{ctrl: ctrl} }

func (m *MockStore) Get(key string) string { return "" }
`)
	write("gomockmocks/adapter.go", scaffold(t, "mock-adapter", []string{"mockgen", "MockStore"}, "gomockmocks"))

	write("cmd/app/main.go", `package main

import "context"

func main() { _, _ = New(context.Background()) }
`)
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/scaffolds/gomockmocks"
	"example.com/scaffolds/mockerymocks"
	"example.com/scaffolds/moqmocks"
	"example.com/scaffolds/redisx"
	"example.com/scaffolds/widget"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*widget.Widget](),
		servo.Root[*redisx.Client](),
		servo.Root[*moqmocks.StoreMock](),
		servo.Root[*mockerymocks.StoreMock](),
		servo.Root[*gomockmocks.MockStoreForServo](),
	)
}
`)
	runGoModTidy(t, dir)

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generating a graph rooted in every scaffold: %v", err)
	}
	for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the scaffolded module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
