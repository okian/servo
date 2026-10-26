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
// `servo list --rejected`, and to the near-miss lines a "no provider"
// diagnostic ends with.
type Rejected struct {
	Pkg    string // import path, so callers can filter to just the main module
	Name   string
	Pos    token.Position
	Reason string
	// Result is what the function would provide were it accepted: its
	// first result, or the non-error one of an (error, T) pair. Every
	// rejection has one, since a function with no results is neither
	// accepted nor rejected. A generic function's may mention its own type
	// parameters; Produces matches those against an instantiation.
	Result types.Type
	// Recv is a method's receiver as written in Name, e.g. "(*Server)";
	// empty for a function. Methods are never providers, and `servo list
	// --rejected` groups them apart from the functions that nearly were.
	Recv string
}

// IsMethod reports whether r is a method rather than a package-level
// function.
func (r Rejected) IsMethod() bool { return r.Recv != "" }

// Produces reports whether a function whose result is result would provide
// want: the identical type, or — for a generic function, whose result
// mentions its own type parameters — want as an instantiation of it, each
// type parameter standing for one type throughout. Constraints are not
// checked; this decides which near misses a diagnostic names, not what
// resolves.
func Produces(result, want types.Type) bool {
	bound := map[*types.TypeParam]types.Type{}
	var match func(p, w types.Type) bool
	match = func(p, w types.Type) bool {
		p, w = types.Unalias(p), types.Unalias(w)
		if tp, ok := p.(*types.TypeParam); ok {
			if prior, seen := bound[tp]; seen {
				return types.Identical(prior, w)
			}
			bound[tp] = w
			return true
		}
		switch p := p.(type) {
		case *types.Pointer:
			w, ok := w.(*types.Pointer)
			return ok && match(p.Elem(), w.Elem())
		case *types.Slice:
			w, ok := w.(*types.Slice)
			return ok && match(p.Elem(), w.Elem())
		case *types.Map:
			w, ok := w.(*types.Map)
			return ok && match(p.Key(), w.Key()) && match(p.Elem(), w.Elem())
		case *types.Named:
			w, ok := w.(*types.Named)
			if !ok || p.Origin().Obj() != w.Origin().Obj() {
				return false
			}
			pa, wa := p.TypeArgs(), w.TypeArgs()
			if pa.Len() != wa.Len() {
				return false
			}
			for i := range pa.Len() {
				if !match(pa.At(i), wa.At(i)) {
					return false
				}
			}
			return true
		default:
			return types.Identical(p, w)
		}
	}
	return match(result, want)
}

// ComparePos orders positions by file, then line, then column, giving the
// candidate-index sort a total order independent of package load order.
// The file is compared as a raw filename, so the order of two files in
// different modules depends on where each was read from; anything whose
// order reaches generated output uses SourceOrder instead.
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
