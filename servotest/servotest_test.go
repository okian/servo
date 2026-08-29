package servotest

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"
)

func TestNoLeaksPassesCleanTest(t *testing.T) {
	defer NoLeaks(t)
}

func TestAssertStopOrderSubsequence(t *testing.T) {
	rec := NewRecorder(servo.StartupReport{}, servo.Report{Nodes: []servo.NodeResult{
		{Name: "*api.Server"},
		{Name: "*postgres.DB"},
		{Name: "*logger.Logger"},
	}})
	AssertStopOrder(t, rec, "*api.Server", "*postgres.DB", "*logger.Logger")
	AssertStopOrder(t, rec, "*api.Server", "*logger.Logger") // valid subsequence, DB interspersed
}

func TestAssertInitOrderSubsequence(t *testing.T) {
	rec := NewRecorder(servo.StartupReport{Nodes: []servo.StartupNode{
		{Type: "*logger.Logger"},
		{Type: "*postgres.DB"},
	}}, servo.Report{})
	AssertInitOrder(t, rec, "*logger.Logger", "*postgres.DB")
}

// isSubsequence carries the actual pass/fail decision for both Assert*
// functions; testing it directly covers the failure cases without
// deliberately failing a t.Run subtest (which — a real gotcha — marks the
// parent test failed too, regardless of what the parent does with the
// bool t.Run returns).
func TestIsSubsequence(t *testing.T) {
	cases := []struct {
		name       string
		got, want  []string
		expectTrue bool
	}{
		{"exact match", []string{"a", "b", "c"}, []string{"a", "b", "c"}, true},
		{"valid subsequence with interspersed elements", []string{"a", "b", "c"}, []string{"a", "c"}, true},
		{"empty want always matches", []string{"a", "b"}, nil, true},
		{"wrong order", []string{"b", "a"}, []string{"a", "b"}, false},
		{"missing element", []string{"a"}, []string{"a", "b"}, false},
		{"empty got with non-empty want", nil, []string{"a"}, false},
	}
	for _, c := range cases {
		if got := isSubsequence(c.got, c.want); got != c.expectTrue {
			t.Errorf("%s: isSubsequence(%v, %v) = %v, want %v", c.name, c.got, c.want, got, c.expectTrue)
		}
	}
}

func TestTimeoutOverridesAndRestoresDefaultStopBudget(t *testing.T) {
	original := servo.DefaultStopBudget
	t.Run("subtest", func(st *testing.T) {
		Timeout(st, 5*time.Millisecond)
		if servo.DefaultStopBudget != 5*time.Millisecond {
			st.Fatalf("DefaultStopBudget = %v, want 5ms", servo.DefaultStopBudget)
		}
	})
	if servo.DefaultStopBudget != original {
		t.Fatalf("DefaultStopBudget not restored after subtest: got %v, want %v", servo.DefaultStopBudget, original)
	}
}

// TestNoNewLeaksIgnoresAPreexistingGoroutine pins the difference between
// the two checks: a goroutine parked before the baseline is taken is not
// this test's leak, and reporting it here is what made NoLeaks fail an
// innocent test that followed a deliberately-abandoned one.
func TestNoNewLeaksIgnoresAPreexistingGoroutine(t *testing.T) {
	release := make(chan struct{})
	parked := make(chan struct{})
	go func() {
		close(parked)
		<-release
	}()
	<-parked
	// Closed only after the check below has run, so the goroutine really
	// is still running at the moment NoNewLeaks looks.
	defer close(release)

	done := NoNewLeaks(t)
	done()
}

// failingAssertionChild marks the second copy of this test binary spawned
// by TestAssertStopOrderFailsWhenTheOrderIsWrong.
const failingAssertionChild = "SERVOTEST_FAILING_ASSERTION_CHILD"

// TestAssertStopOrderFailsWhenTheOrderIsWrong makes the one assertion the
// isSubsequence table cannot. That table proves the decision is right;
// nothing there proves AssertStopOrder acts on it. Delete the body of the
// `if !isSubsequence` in assertSubsequence and every other test in this
// package still passes, while every caller's stop-order guarantee quietly
// stops being checked — the worst kind of broken assertion, one that only
// ever agrees with you.
//
// It cannot be made in process, for the reason TestIsSubsequence above
// gives: t.Fatalf fails the *testing.T it is handed, and a subtest's
// failure reaches its parent whatever the parent does with the bool t.Run
// returns. So the failing assertion runs in a second copy of this binary
// and the parent reads its exit status and output.
func TestAssertStopOrderFailsWhenTheOrderIsWrong(t *testing.T) {
	if os.Getenv(failingAssertionChild) == "1" {
		// The child half. This app stopped its database before the server
		// still serving requests against it, which is the inversion a
		// stop-order guarantee exists to catch. Returning normally means
		// AssertStopOrder waved it through, and the child exits 0 — which
		// is precisely what the parent below treats as the failure.
		rec := NewRecorder(servo.StartupReport{}, servo.Report{Nodes: []servo.NodeResult{
			{Name: "*postgres.DB"},
			{Name: "*api.Server"},
		}})
		AssertStopOrder(t, rec, "*api.Server", "*postgres.DB")
		return
	}

	args := []string{"-test.run=^" + t.Name() + "$"}
	// The assertion under test runs in the child, so its coverage counters
	// are the child's and never reach this profile — the line reads as
	// uncovered even though it genuinely ran. Forwarding the parent's
	// -test.gocoverdir would fix the number, and is deliberately not done
	// here: it is an undocumented internal flag, and a coverage figure
	// propped up by plumbing is worth less than one that is simply honest
	// about what it can see. The test's value is the assertion below, not
	// the counter.
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), failingAssertionChild+"=1")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("the child passed: AssertStopOrder accepted a stop order that inverts the one asked for\n%s", out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("could not run the child copy of this test binary: %v\n%s", err, out)
	}

	// Both sequences have to appear. "wrong stop order" on its own leaves
	// the reader to reconstruct what the app actually did from a report
	// the failure did not print.
	for _, want := range []string{
		"stop order",
		"[*postgres.DB *api.Server]",
		"[*api.Server *postgres.DB]",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the child's failure does not mention %q, so it does not say what went wrong:\n%s", want, out)
		}
	}
}
