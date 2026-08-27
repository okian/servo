package servotest_test

import (
	"fmt"

	"github.com/okian/servo/v3/servo"
	"github.com/okian/servo/v3/servotest"
)

// ExampleNewRecorder wraps a generated app's own init and shutdown reports
// so their node ordering can be inspected directly. AssertInitOrder and
// AssertStopOrder do this same subsequence check against a *testing.T,
// failing the test on a mismatch instead of just reporting one — they need
// a real *testing.T, so they aren't shown here.
func ExampleNewRecorder() {
	init := servo.StartupReport{Nodes: []servo.StartupNode{
		{Type: "*config.Loader"},
		{Type: "*postgres.DB"},
		{Type: "*api.Server"},
	}}
	shutdown := servo.Report{Nodes: []servo.NodeResult{
		{Name: "*api.Server", Status: servo.StatusOK},
		{Name: "*postgres.DB", Status: servo.StatusOK},
	}}

	rec := servotest.NewRecorder(init, shutdown)
	fmt.Println(len(rec.Init.Nodes), len(rec.Shutdown.Nodes))
	// Output: 3 2
}
