package servo

import (
	"errors"
	"fmt"
	"strings"
)

// NodeStatus is the terminal outcome of one node's init or stop attempt.
type NodeStatus int

const (
	StatusOK NodeStatus = iota
	StatusFailed
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

// NodeResult is one node's outcome, keyed by its type string.
type NodeResult struct {
	Name   string
	Status NodeStatus
	Err    error
}

// Report enumerates every node's outcome for one Init or Shutdown pass, in
// the order the framework processed them.
type Report struct {
	Nodes []NodeResult
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

// MergeNodeResults combines a node's per-phase results (e.g. Drain, Flush,
// Close) into the single per-node outcome Report enumerates: abandoned
// outranks failed outranks ok, and every phase error is joined.
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
