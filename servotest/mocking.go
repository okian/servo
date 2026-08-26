package servotest

import "fmt"

// PanicReporter satisfies go.uber.org/mock/gomock's TestReporter interface
// (Errorf/Fatalf) structurally, without servotest importing gomock at all.
// It exists because *gomock.Controller needs a reporter and there is no
// *testing.T reachable from inside a generated graph — construct one with
// gomock.NewController(servotest.PanicReporter{}) inside a mock adapter's
// constructor (see the README's Mocking section).
//
// A failed expectation therefore panics rather than calling t.Fatalf; Go's
// test runner still reports which test failed before the process exits,
// but the process does exit, unlike a clean, isolated t.Fatalf failure.
// For strict expectation-count verification where that distinction
// matters, construct and drive the mock directly in an isolated unit test
// with a real *testing.T instead of through servo.Override.
type PanicReporter struct{}

func (PanicReporter) Errorf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }
func (PanicReporter) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }
