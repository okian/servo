package servo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// TestReportErr covers the comparison a Report in an error variable gets
// wrong: by value it is never nil, so `if err != nil` holds even for a
// perfectly clean shutdown. Err is nil exactly when the report is clean.
func TestReportErr(t *testing.T) {
	if err := (Report{}).Err(); err != nil {
		t.Fatalf("empty report: Err() = %v, want nil", err)
	}
	if err := (Report{Nodes: []NodeResult{{Name: "a", Status: StatusOK}}}).Err(); err != nil {
		t.Fatalf("clean report: Err() = %v, want nil", err)
	}

	stuck := errors.New("stuck")
	err := Report{Nodes: []NodeResult{{Name: "*postgres.DB", Status: StatusAbandoned, Err: stuck}}}.Err()
	if err == nil || !errors.Is(err, stuck) || err.Error() != "*postgres.DB: abandoned: stuck" {
		t.Fatalf("dirty report: Err() = %v, want the report itself", err)
	}
}

func TestNodeError(t *testing.T) {
	refused := errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
	var err error = &NodeError{Node: "*postgres.DB", Phase: "Init", Err: refused}

	if got, want := err.Error(), "*postgres.DB: Init: dial tcp 127.0.0.1:5432: connect: connection refused"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	// Code matching the component's own errors keeps working through it.
	wrapped := fmt.Errorf("startup: %w", err)
	if !errors.Is(wrapped, refused) {
		t.Fatal("errors.Is does not reach the node's own error")
	}
	var ne *NodeError
	if !errors.As(wrapped, &ne) || ne.Node != "*postgres.DB" || ne.Phase != "Init" {
		t.Fatalf("errors.As = %+v, want the node and phase", ne)
	}
	// A NodeError built without an Err still renders, rather than
	// panicking inside whatever is logging it.
	if got := (&NodeError{Node: "*a.A", Phase: "Run"}).Error(); got != "*a.A: Run: failed" {
		t.Fatalf("Error() with no Err = %q", got)
	}
}

// TestReportJSON is the R2 item: the default encoding wrote the status as
// a bare number and every error as {}, so a report served from a health
// endpoint said a node failed and never why.
func TestReportJSON(t *testing.T) {
	r := Report{Nodes: []NodeResult{
		{Name: "*api.Server", Status: StatusOK},
		{Name: "*postgres.DB", Status: StatusAbandoned, Err: context.DeadlineExceeded},
		{Name: "*redis.Cache", Status: StatusFailed, Err: errors.New(`said "no"`)},
	}}
	got, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"nodes":[{"name":"*api.Server","status":"ok"},{"name":"*postgres.DB","status":"abandoned","err":"context deadline exceeded"},{"name":"*redis.Cache","status":"failed","err":"said \"no\""}]}`
	if string(got) != want {
		t.Fatalf("json.Marshal =\n%s\nwant\n%s", got, want)
	}

	// The same shape behind a pointer, as encoding/json reaches it inside
	// a struct field of type *NodeResult.
	if b, _ := json.Marshal(&r.Nodes[1]); string(b) != `{"name":"*postgres.DB","status":"abandoned","err":"context deadline exceeded"}` {
		t.Fatalf("json.Marshal(*NodeResult) = %s", b)
	}

	var back Report
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(back.Nodes) != 3 || back.Nodes[0].Err != nil || back.Nodes[1].Status != StatusAbandoned || back.Nodes[2].Err == nil || back.Nodes[2].Err.Error() != `said "no"` {
		t.Fatalf("round trip = %+v", back)
	}
	if back.Clean() || back.Error() != r.Error() {
		t.Fatalf("round trip renders %q, want %q", back.Error(), r.Error())
	}
}

// TestNodeStatusDecodesItsOldForm: before statuses had a text form they
// were encoded as numbers, and encoding/json refuses a number for any type
// that implements encoding.TextUnmarshaler. Stored reports must keep
// decoding.
func TestNodeStatusDecodesItsOldForm(t *testing.T) {
	var r Report
	if err := json.Unmarshal([]byte(`{"Nodes":[{"Name":"a","Status":0,"Err":null},{"Name":"b","Status":2,"Err":null}]}`), &r); err != nil {
		t.Fatalf("json.Unmarshal of the pre-3.5 encoding: %v", err)
	}
	if len(r.Nodes) != 2 || r.Nodes[0].Status != StatusOK || r.Nodes[1].Status != StatusAbandoned || r.Nodes[1].Name != "b" {
		t.Fatalf("decoded %+v", r)
	}

	for _, bad := range []string{`"exploded"`, `true`} {
		var s NodeStatus
		if err := json.Unmarshal([]byte(bad), &s); err == nil {
			t.Errorf("json.Unmarshal(%s) into NodeStatus = nil error, want a refusal", bad)
		}
	}

	// As text — a map key, a flag, a YAML scalar — too.
	text, err := StatusFailed.MarshalText()
	if err != nil || string(text) != "failed" {
		t.Fatalf("MarshalText = %q, %v", text, err)
	}
	var s NodeStatus
	if err := s.UnmarshalText(text); err != nil || s != StatusFailed {
		t.Fatalf("UnmarshalText(%q) = %v, %v", text, s, err)
	}
	if b, _ := json.Marshal(map[NodeStatus]int{StatusAbandoned: 3}); string(b) != `{"abandoned":3}` {
		t.Fatalf("as a map key: %s", b)
	}
}

// TestReportJSONRefusesAMalformedNode: a stored report whose node has a
// field of the wrong type fails to decode, rather than coming back as a
// node with that field silently left empty. NodeStatus.UnmarshalJSON is
// exported, so it can be handed bytes encoding/json never would: a quoted
// status cut short is refused too, not read as a name.
func TestReportJSONRefusesAMalformedNode(t *testing.T) {
	for _, bad := range []string{
		`{"nodes":[{"name":5,"status":"ok"}]}`,
		`{"nodes":[{"name":"*postgres.DB","status":"failed","err":7}]}`,
	} {
		var r Report
		if err := json.Unmarshal([]byte(bad), &r); err == nil {
			t.Errorf("json.Unmarshal(%s) = nil error, decoded %+v; want a refusal", bad, r)
		}
	}
	var s NodeStatus
	if err := s.UnmarshalJSON([]byte(`"fail`)); err == nil {
		t.Errorf("UnmarshalJSON of an unterminated string = nil error, status %v", s)
	}
}
