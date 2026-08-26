// Package graph defines the resolved-graph data model: canonical type
// identity, provider candidates, and structural capability detection.
package graph

import "go/types"

// Key is the canonical identity of a value in the object graph: a fully
// qualified type string (after types.Unalias, so aliases collapse to their
// target) plus an optional tag. T and *T are distinct keys because their
// type strings differ by the leading "*".
type Key struct {
	Type string
	Tag  string
}

func (k Key) String() string {
	if k.Tag == "" {
		return k.Type
	}
	return k.Type + "#" + k.Tag
}

// NewKey builds the canonical Key for t. Passing a non-empty tag
// distinguishes multiple bindings of the same type.
func NewKey(t types.Type, tag string) Key {
	return Key{Type: TypeString(t), Tag: tag}
}

// TypeString renders t with full import paths (never a bare local name and
// never abbreviated), after resolving aliases, so it is stable across
// packages and safe to use as a map key or diagnostic identifier.
func TypeString(t types.Type) string {
	return types.TypeString(types.Unalias(t), fullPathQualifier)
}

func fullPathQualifier(pkg *types.Package) string {
	return pkg.Path()
}
