package resolve

import (
	"go/types"

	"github.com/okian/servo/v3/internal/graph"
)

// selection is the outcome of applying provider-selection precedence for
// one requested key: explicit Bind, then a unique exact-type candidate,
// then a unique structural interface implementation.
type selection struct {
	provider *graph.Provider
	binding  string // set only when provider != nil

	// Populated only on failure, to build a diagnostic:
	requested   graph.Key // the key to report as unresolved (may be the Bind target, not k)
	candidates  []*graph.Provider
	isInterface bool // whether "requested" is a non-empty interface (changes diagnostic phrasing)
}

func (r *resolver) selectProvider(k graph.Key, kType types.Type) selection {
	effective := k
	explicitUsed := false
	if concrete, ok := r.explicit[k]; ok {
		effective = concrete
		explicitUsed = true
	}

	switch exact := r.byResult[effective]; len(exact) {
	case 1:
		binding := "sole candidate"
		if explicitUsed {
			binding = "explicit bind"
		}
		return selection{provider: exact[0], binding: binding}
	case 0:
		if explicitUsed {
			return selection{requested: effective, isInterface: false}
		}
		// fall through to structural search below
	default:
		return selection{requested: effective, candidates: exact, isInterface: false}
	}

	iface, ok := interfaceOf(kType)
	if !ok {
		return selection{requested: k, isInterface: false}
	}

	var structural []*graph.Provider
	for _, c := range r.all {
		if !r.scope[c.Pkg] {
			continue
		}
		if types.Implements(c.ResultType, iface) {
			structural = append(structural, c)
		}
	}
	switch len(structural) {
	case 1:
		return selection{provider: structural[0], binding: "sole implementation"}
	case 0:
		return selection{requested: k, isInterface: true}
	default:
		return selection{requested: k, candidates: structural, isInterface: true}
	}
}

func interfaceOf(t types.Type) (*types.Interface, bool) {
	iface, ok := types.Unalias(t).Underlying().(*types.Interface)
	if !ok || iface.NumMethods() == 0 {
		return nil, false
	}
	return iface, true
}
