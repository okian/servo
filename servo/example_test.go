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
