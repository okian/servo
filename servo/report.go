package servo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// NodeStatus is the terminal outcome of one node's init or stop attempt.
type NodeStatus int

const (
	// StatusOK: the call returned nil within its budget.
	StatusOK NodeStatus = iota
	// StatusFailed: the call returned an error, or panicked, within its
	// budget.
	StatusFailed
	// StatusAbandoned: the call did not return within its budget and was
	// left running — or it was never started, because the context it
	// would have run under was already done.
	StatusAbandoned
)

func (s NodeStatus) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusFailed:
		return "failed"
	case StatusAbandoned:
		return "abandoned"
	default:
		return "unknown"
	}
}

// MarshalText encodes a status as its String form, so a report written
// out as JSON, or logged by a structured logger, says "abandoned" rather
// than a bare 2 that means nothing without this file open beside it.
func (s NodeStatus) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

// UnmarshalText is MarshalText's inverse: it accepts "ok", "failed" and
// "abandoned", and refuses anything else rather than guess.
func (s *NodeStatus) UnmarshalText(text []byte) error {
	for _, candidate := range []NodeStatus{StatusOK, StatusFailed, StatusAbandoned} {
		if string(text) == candidate.String() {
			*s = candidate
			return nil
		}
	}
	return fmt.Errorf("servo: unknown node status %q", text)
}

// UnmarshalJSON accepts the quoted names MarshalText writes and, as well,
// the bare numbers a status was encoded as before it had a text form.
// encoding/json refuses a number for any type implementing
// encoding.TextUnmarshaler, so without this a report stored by an earlier
// release would stop decoding the moment the names were introduced.
func (s *NodeStatus) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		return s.UnmarshalText([]byte(text))
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("servo: node status must be a name or a number: %w", err)
	}
	*s = NodeStatus(n)
	return nil
}

// NodeResult is one node's outcome, keyed by its type string.
type NodeResult struct {
	Name   string
	Status NodeStatus
	Err    error
}

// nodeResultJSON is NodeResult's wire form. Err is a pointer so that a nil
// error and an error whose message happens to be empty stay distinct
// across a round trip.
type nodeResultJSON struct {
	Name   string     `json:"name"`
	Status NodeStatus `json:"status"`
	Err    *string    `json:"err,omitempty"`
}

// MarshalJSON writes {"name": …, "status": "failed", "err": "…"}, with err
// omitted when there is none. The default encoding turned every error into
// {} — an error is an interface whose concrete types rarely export a field
// — so a report served from a health endpoint said which node failed and
// never why.
func (n NodeResult) MarshalJSON() ([]byte, error) {
	out := nodeResultJSON{Name: n.Name, Status: n.Status}
	if n.Err != nil {
		msg := n.Err.Error()
		out.Err = &msg
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads what MarshalJSON writes. The error comes back as an
// errors.New of its message: its type, and anything errors.Is could have
// matched, did not survive the encoding and cannot be recovered from it.
func (n *NodeResult) UnmarshalJSON(data []byte) error {
	var in nodeResultJSON
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	*n = NodeResult{Name: in.Name, Status: in.Status}
	if in.Err != nil {
		n.Err = errors.New(*in.Err)
	}
	return nil
}

// Report enumerates every node's outcome for one Shutdown, Health or Ready
// pass, in the order the framework processed them. It encodes to JSON as
// {"nodes": [...]}, each node as NodeResult.MarshalJSON writes it.
type Report struct {
	Nodes []NodeResult `json:"nodes"`
}

// Clean reports whether every node reached StatusOK.
func (r Report) Clean() bool {
	for _, n := range r.Nodes {
		if n.Status != StatusOK {
			return false
		}
	}
	return true
}

// Err returns the report as an error when it is not clean, and nil when it
// is.
//
// It exists for the comparison Go code reaches for first. Report satisfies
// error by value, so a Report stored in an error variable is never nil —
// not even for a shutdown that went perfectly — and `if err != nil` on it
// is always true. Err is the form of the same report that answers that
// comparison honestly.
func (r Report) Err() error {
	if r.Clean() {
		return nil
	}
	return r
}

// Error renders every non-OK node as a single message. Report satisfies the
// error interface so it composes with errors.Join and %v/%w formatting.
func (r Report) Error() string {
	var b strings.Builder
	for _, n := range r.Nodes {
		if n.Status == StatusOK {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s: %s", n.Name, n.Status)
		if n.Err != nil {
			fmt.Fprintf(&b, ": %v", n.Err)
		}
	}
	return b.String()
}

// Unwrap exposes each node's error for errors.Is/errors.As traversal.
func (r Report) Unwrap() []error {
	var errs []error
	for _, n := range r.Nodes {
		if n.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", n.Name, n.Err))
		}
	}
	return errs
}

// NodeError is one node's failure in one lifecycle phase: the error a
// generated New returns when an Init fails, and Run returns when a runner
// fails or panics.
//
// Node is the node's type string, the same one Report, App.Graph and
// `servo explain` use. Phase is the lifecycle method that failed — "Init"
// or "Run". Err is what that method returned, or, for a panic, an error
// carrying the panic value and the stack it was raised on.
//
// It exists so a startup failure names its node. An Init error used to be
// returned exactly as the component produced it, and "dial tcp
// 127.0.0.1:5432: connect: connection refused" does not say which of
// several components that dial was made by. errors.Is and errors.As see
// through it to Err, so code matching on the component's own errors keeps
// working unchanged; match a *NodeError with errors.As when the node is
// what you need.
type NodeError struct {
	Node  string
	Phase string
	Err   error
}

// Error renders "<node>: <phase>: <err>".
func (e *NodeError) Error() string {
	if e.Err == nil {
		return e.Node + ": " + e.Phase + ": failed"
	}
	return e.Node + ": " + e.Phase + ": " + e.Err.Error()
}

// Unwrap returns Err, so errors.Is and errors.As traverse into it.
func (e *NodeError) Unwrap() error { return e.Err }

// MergeNodeResults combines a node's per-phase results (e.g. Drain, Flush,
// Close) into the single per-node outcome Report enumerates: abandoned
// outranks failed outranks ok, and every phase error is joined.
//
// Deprecated: generated code calls servoimpl.MergeNodeResults since
// v3.5.0; this remains for files generated earlier.
func MergeNodeResults(name string, results ...NodeResult) NodeResult {
	merged := NodeResult{Name: name, Status: StatusOK}
	var errs []error
	for _, res := range results {
		switch {
		case res.Status == StatusAbandoned:
			merged.Status = StatusAbandoned
		case res.Status == StatusFailed && merged.Status != StatusAbandoned:
			merged.Status = StatusFailed
		}
		if res.Err != nil {
			errs = append(errs, res.Err)
		}
	}
	if len(errs) > 0 {
		merged.Err = errors.Join(errs...)
	}
	return merged
}

var _ error = Report{}
