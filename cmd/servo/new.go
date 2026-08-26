package main

import "fmt"

// runNew scaffolds a component, adapter, or mock adapter — plain Go
// printed to stdout, never importing servo, so the caller decides where
// the file lands.
func runNew(kind string, args []string) error {
	switch kind {
	case "component":
		if len(args) != 1 {
			return fmt.Errorf("usage: servo new component <Name>")
		}
		printComponentScaffold(args[0])
		return nil
	case "adapter":
		if len(args) != 1 {
			return fmt.Errorf("usage: servo new adapter <pkgname>")
		}
		printAdapterScaffold(args[0])
		return nil
	case "mock-adapter":
		if len(args) != 2 {
			return fmt.Errorf("usage: servo new mock-adapter <moq|mockery|gomock> <GeneratedTypeName>")
		}
		return printMockAdapterScaffold(args[0], args[1])
	default:
		return fmt.Errorf("servo new: unknown kind %q (want component|adapter|mock-adapter)", kind)
	}
}

func printComponentScaffold(name string) {
	fmt.Printf(`type %s struct{}

func New%s() *%s {
	return &%s{}
}

// Uncomment whichever capabilities %s needs — detected structurally by
// servo generate, no registration required.
// func (c *%s) Init(ctx context.Context) error   { return nil }
// func (c *%s) Run(ctx context.Context) error    { return nil }
// func (c *%s) Drain(ctx context.Context) error  { return nil }
// func (c *%s) Flush(ctx context.Context) error  { return nil }
// func (c *%s) Stop(ctx context.Context) error   { return nil }
// func (c *%s) Health(ctx context.Context) error { return nil }
// func (c *%s) Ready(ctx context.Context) error  { return nil }
`, name, name, name, name, name, name, name, name, name, name, name, name)
}

// printMockAdapterScaffold prints the adapter file every mock integration
// needs: none of moq, mockery, or gomock generate a provider shape servo
// can call directly (see the README's Mocking section for why), and
// rediscovering the fix for each tool independently is exactly the
// friction this scaffold exists to remove.
func printMockAdapterScaffold(tool, typeName string) error {
	switch tool {
	case "moq":
		printMoqAdapterScaffold(typeName)
	case "mockery":
		printMockeryAdapterScaffold(typeName)
	case "gomock":
		printGomockAdapterScaffold(typeName)
	default:
		return fmt.Errorf("servo new mock-adapter: unknown tool %q (want moq|mockery|gomock)", tool)
	}
	return nil
}

// printMoqAdapterScaffold covers moq: it generates a plain struct with
// function fields and no constructor at all, so the fix is just adding one.
func printMoqAdapterScaffold(typeName string) {
	fmt.Printf(`// %s is moq-generated (no constructor of its own).
func New%s() *%s { return &%s{} }
`, typeName, typeName, typeName, typeName)
}

// printMockeryAdapterScaffold covers mockery: its generated constructor
// takes a *testing.T-shaped value to auto-register AssertExpectations,
// which is itself a valid provider shape servo could call — making a
// second, zero-arg constructor for the same type a genuine ambiguity. The
// fix is a distinct wrapper type, not a second constructor.
func printMockeryAdapterScaffold(typeName string) {
	fmt.Printf(`// %sMock embeds mockery's generated *%s — a distinct result
// type, so it never collides with %s's own constructor.
type %sMock struct{ *%s }

func New%sMock() *%sMock { return &%sMock{%s: &%s{}} }
`, typeName, typeName, typeName, typeName, typeName, typeName, typeName, typeName, typeName, typeName)
}

// printGomockAdapterScaffold covers gomock: same wrapping requirement as
// mockery, for the same reason, plus *gomock.Controller itself needing a
// TestReporter with no *testing.T reachable from inside the graph —
// servotest.PanicReporter supplies one without adding a gomock dependency
// to servotest itself.
func printGomockAdapterScaffold(typeName string) {
	fmt.Printf(`// %sForServo wraps gomock-generated *%s. Needs:
//   "go.uber.org/mock/gomock"
//   "github.com/okian/servo/v2/servotest"
type %sForServo struct {
	*%s
	Finish func()
}

func New%sForServo() *%sForServo {
	ctrl := gomock.NewController(servotest.PanicReporter{})
	return &%sForServo{%s: New%s(ctrl), Finish: ctrl.Finish}
}

// Finish must be called directly by the test (a plain defer), right after
// construction — never routed through servo's (T, func()) cleanup shape,
// which would run it inside servo.RunStop's own goroutine during Shutdown
// and panic there instead of in the test's own goroutine.
`, typeName, typeName, typeName, typeName, typeName, typeName, typeName, typeName, typeName)
}

func printAdapterScaffold(pkgName string) {
	fmt.Printf(`package %s

import "context"

type Config struct {
	// Addr, Timeout, Credentials, ...
}

type Client struct {
	cfg Config
}

func New(cfg Config) (*Client, func(), error) {
	client := &Client{cfg: cfg}
	cleanup := func() {}
	return client, cleanup, nil
}

func (c *Client) Stop(ctx context.Context) error {
	return nil
}

func (c *Client) Health(ctx context.Context) error {
	return nil
}
`, pkgName)
}
