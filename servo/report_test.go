package servo

import (
	"errors"
	"testing"
)

func TestNodeStatusString(t *testing.T) {
	cases := []struct {
		status NodeStatus
		want   string
	}{
		{StatusOK, "ok"},
		{StatusFailed, "failed"},
		{StatusAbandoned, "abandoned"},
		{NodeStatus(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.status.String(); got != c.want {
			t.Errorf("NodeStatus(%d).String() = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestReportClean(t *testing.T) {
	clean := Report{Nodes: []NodeResult{{Name: "a", Status: StatusOK}}}
	if !clean.Clean() {
		t.Fatalf("expected clean report")
	}

	dirty := Report{Nodes: []NodeResult{{Name: "a", Status: StatusOK}, {Name: "b", Status: StatusFailed}}}
	if dirty.Clean() {
		t.Fatalf("expected dirty report")
	}
}

func TestReportErrorAndUnwrap(t *testing.T) {
	errB := errors.New("boom")
	r := Report{Nodes: []NodeResult{
		{Name: "a", Status: StatusOK},
		{Name: "b", Status: StatusFailed, Err: errB},
		{Name: "c", Status: StatusAbandoned},
	}}

	if got, want := r.Error(), "b: failed: boom; c: abandoned"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}

	unwrapped := r.Unwrap()
	if len(unwrapped) != 1 {
		t.Fatalf("Unwrap() = %v, want 1 error (c has no Err)", unwrapped)
	}
	if !errors.Is(unwrapped[0], errB) {
		t.Fatalf("Unwrap()[0] does not wrap %v", errB)
	}

	var asErr error = r
	if asErr.Error() == "" {
		t.Fatalf("Report must satisfy error with non-empty message when dirty")
	}
}

func TestMergeNodeResults(t *testing.T) {
	errA := errors.New("a failed")
	errB := errors.New("b abandoned")

	t.Run("all ok", func(t *testing.T) {
		m := MergeNodeResults("x", NodeResult{Status: StatusOK}, NodeResult{Status: StatusOK})
		if m.Status != StatusOK || m.Err != nil {
			t.Fatalf("got %+v, want clean ok", m)
		}
	})

	t.Run("failed outranks ok", func(t *testing.T) {
		m := MergeNodeResults("x", NodeResult{Status: StatusOK}, NodeResult{Status: StatusFailed, Err: errA})
		if m.Status != StatusFailed || !errors.Is(m.Err, errA) {
			t.Fatalf("got %+v", m)
		}
	})

	t.Run("abandoned outranks failed", func(t *testing.T) {
		m := MergeNodeResults("x",
			NodeResult{Status: StatusFailed, Err: errA},
			NodeResult{Status: StatusAbandoned, Err: errB},
		)
		if m.Status != StatusAbandoned {
			t.Fatalf("got status %v, want abandoned", m.Status)
		}
		if !errors.Is(m.Err, errA) || !errors.Is(m.Err, errB) {
			t.Fatalf("expected joined error to wrap both, got %v", m.Err)
		}
	})
}
