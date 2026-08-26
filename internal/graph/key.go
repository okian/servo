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
	return types.TypeString(unaliasDeep(t), fullPathQualifier)
}

// unaliasDeep resolves t's own alias chain, then recurses through pointer
// indirection: types.Unalias only resolves t itself, not aliases nested
// inside it, so plain types.Unalias(*Alias) leaves the pointer's element
// unresolved and "*Alias"/"*Underlying" compute to different strings even
// though they are the identical type (types.Identical agrees; only the
// rendered string disagreed).
func unaliasDeep(t types.Type) types.Type {
	u := types.Unalias(t)
	if ptr, ok := u.(*types.Pointer); ok {
		return types.NewPointer(unaliasDeep(ptr.Elem()))
	}
	return u
}

func fullPathQualifier(pkg *types.Package) string {
	return pkg.Path()
}
