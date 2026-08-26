package main

import "fmt"

// runNew scaffolds a component or adapter — plain Go printed to stdout,
// never importing servo, so the caller decides where the file lands.
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
	default:
		return fmt.Errorf("servo new: unknown kind %q (want component|adapter)", kind)
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
