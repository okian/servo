package servotest

import (
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
