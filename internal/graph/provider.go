package graph

import (
	"go/token"
	"go/types"
)

// Provider is one accepted candidate constructor.
type Provider struct {
	Result     Key
	ResultType types.Type // concrete type, for types.Implements checks
	Params     []Key
	ParamTypes []types.Type // parallel to Params; a param's real type, for interface auto-bind checks
	Func       *types.Func
	Pkg        string // import path declaring Func
	Name       string // qualified name, e.g. "postgres.NewDB", for diagnostics and var naming
	Pos        token.Position
	HasCleanup bool
	HasError   bool
	Unexported bool
}

// Rejected records a function that looked like it might be a provider (it
// has at least one result) but was excluded, and why — the input to
// `servo list --rejected`.
type Rejected struct {
	Pkg    string // import path, so callers can filter to just the main module
	Name   string
	Pos    token.Position
	Reason string
}

// ComparePos orders positions by file, then line, then column, giving the
// candidate-index sort a total order independent of package load order.
func ComparePos(a, b token.Position) int {
	if a.Filename != b.Filename {
		if a.Filename < b.Filename {
			return -1
		}
		return 1
	}
	if a.Line != b.Line {
		return a.Line - b.Line
	}
	return a.Column - b.Column
}
