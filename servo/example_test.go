package servo_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/okian/servo/v3/servo"
)

// Store, Postgres, FakeStore, and Server stand in for a real application's
// own types, so the examples below can show what a spec file — a file
// gated by the servoinject build tag, excluded from the compiled binary —
// looks like when it references them.

type Store interface {
	Get(key string) string
}

type Postgres struct{}

func (p *Postgres) Get(key string) string { return "" }

type FakeStore struct{}

func (f *FakeStore) Get(key string) string { return "fake" }

type Server struct{}

// ExampleBuild shows a spec file's shape. servo generate reads this call
// as syntax; Build, Root, and Bind all panic if actually executed, which
// only happens if the servoinject build tag is missing from the file. This
// example has no "Output:" comment, so go test compiles it — checking that
// the syntax shown is valid — but never runs it.
func ExampleBuild() {
	servo.Build(
		servo.Root[*Server](),
		servo.Bind[Store, *Postgres](),
	)
}

// ExampleOverride shows a test-only replacement declared alongside Build's
// normal bindings. servo generate emits a NewTestApp constructor that
// wires FakeStore in Store's place instead of Postgres; New is unaffected.
// Like ExampleBuild, this is compiled but never run.
func ExampleOverride() {
	servo.Build(
		servo.Root[*Server](),
		servo.Bind[Store, *Postgres](),
		servo.Override[Store, *FakeStore](),
	)
}

func ExampleReport_Clean() {
	report := servo.Report{Nodes: []servo.NodeResult{
		{Name: "*api.Server", Status: servo.StatusOK},
		{Name: "*postgres.DB", Status: servo.StatusOK},
	}}
	fmt.Println(report.Clean())
	// Output: true
}

func ExampleReport_Error() {
	report := servo.Report{Nodes: []servo.NodeResult{
		{Name: "*api.Server", Status: servo.StatusOK},
		{Name: "*postgres.DB", Status: servo.StatusFailed, Err: errors.New("connection refused")},
	}}
	fmt.Println(report.Error())
	// Output: *postgres.DB: failed: connection refused
}

func ExampleMergeNodeResults() {
	merged := servo.MergeNodeResults("*api.Server",
		servo.NodeResult{Status: servo.StatusOK},
		servo.NodeResult{Status: servo.StatusAbandoned, Err: errors.New("stop timed out")},
	)
	fmt.Println(merged.Status)
	// Output: abandoned
}

func ExampleRunStop() {
	result := servo.RunStop(context.Background(), time.Second, "*api.Server", func(context.Context) error {
		return nil
	})
	fmt.Println(result.Status)
	// Output: ok
}

// App and New stand in for what servo generate writes beside the spec
// file: the App type, with its Run and Shutdown, and its constructor.
type App struct{}

func (*App) Run(context.Context) error             { return nil }
func (*App) Shutdown(context.Context) servo.Report { return servo.Report{} }

func New(context.Context) (*App, error) { return &App{}, nil }

// ExampleMain is the whole of a service's main function. It has no
// "Output:" comment, so go test compiles it — checking that the generated
// New fits Main as shown — but never runs it: Main ends the process.
func ExampleMain() {
	servo.Main(New)
}

// ExampleShutdownTimeout gives the stop longer than the default 25
// seconds, for a deployment whose termination grace period has room for
// it. Compiled, not run, like ExampleMain.
func ExampleShutdownTimeout() {
	servo.Main(New, servo.ShutdownTimeout(50*time.Second))
}

// ExampleExitCode shows the decision a hand-written main hands to os.Exit
// as its last line, once Run has returned and Shutdown has reported.
func ExampleExitCode() {
	clean := servo.Report{Nodes: []servo.NodeResult{{Name: "*api.Server", Status: servo.StatusOK}}}
	dirty := servo.Report{Nodes: []servo.NodeResult{{Name: "*postgres.DB", Status: servo.StatusAbandoned}}}

	fmt.Println(servo.ExitCode(context.Canceled, clean)) // stopped by the signal
	fmt.Println(servo.ExitCode(errors.New("consumer: broker unreachable"), clean))
	fmt.Println(servo.ExitCode(nil, dirty))
	// Output:
	// 0
	// 1
	// 1
}
