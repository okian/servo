package resolve

import (
	"go/types"

	"github.com/okian/servo/v3/internal/graph"
)

// selection is the outcome of applying provider-selection precedence for
// one requested key: explicit Bind, then a unique exact-type candidate —
// the main module's before anyone else's — then a unique structural
// interface implementation.
type selection struct {
	provider *graph.Provider
	binding  string // set only when provider != nil

	// Populated only on failure, to build a diagnostic:
	requested     graph.Key  // the key to report as unresolved (may be the Bind target, not k)
	requestedType types.Type // requested's type, nil when unknown
	// candidates is the ambiguity: two or more providers precedence could
	// not choose between. Empty for a missing provider.
	candidates  []*graph.Provider
	isInterface bool // whether "requested" is a non-empty interface (changes diagnostic phrasing)
	// foreign marks candidates that are all exact matches declared outside
	// the main module: functions the user cannot remove or rename, so the
	// diagnostic offers what they can do instead.
	foreign bool
	// unusable counts the exact-type candidates this injector cannot
	// reference (see reach): the type has a provider, just not one the
	// generated code could call, which the diagnostic's heading says.
	unusable int
	// explicit marks a requested key that a Bind or an Override named.
	explicit bool
}

func (r *resolver) selectProvider(k graph.Key, kType types.Type) selection {
	effective, effectiveType := k, kType
	explicitUsed := false
	if concrete, ok := r.explicit[k]; ok {
		effective, effectiveType = concrete, r.explicitType[k]
		explicitUsed = true
	}

	// Exact type first, and the main module's constructors before anyone
	// else's. The scan covers every loaded package, so a type the standard
	// library or a dependency also constructs — *slog.Logger, *sql.DB,
	// *http.ServeMux — arrives with candidates the user does not own, and
	// counting them alongside the module's own turned a module's one
	// NewLogger into a three-way ambiguity whose advice, "remove or rename
	// all but one", was not advice anyone could take. The module's own
	// constructors decide; a lone outside one is still selected when the
	// module has none, as it always was; several outside ones wait until
	// the module's own implementations of an interface have had their say,
	// below, and are then a diagnostic that says what to write instead.
	//
	// A candidate the injector cannot reference — internal to another
	// tree, an unexported result type, a package that imports the injector
	// — is set aside before any of this: selected, it would only have been
	// a generated file the compiler refuses.
	exact := r.byResult[effective]
	var own, foreign []*graph.Provider
	unusable := 0
	for _, c := range exact {
		switch {
		case r.reach.providerProblem(c) != "":
			unusable++
		case r.scope[c.Pkg]:
			own = append(own, c)
		default:
			foreign = append(foreign, c)
		}
	}
	// The tracer provider is the one dependency servo asks for on the
	// user's behalf, and the OpenTelemetry packages return its type from
	// several functions of their own — the global getter, the no-op
	// provider. Even a lone one of those is not the provider the user
	// meant, so this key is never answered from outside the module: its
	// own constructors, else its own implementations.
	if r.traceIn != nil && effective == r.traceIn.ProviderKey {
		foreign = nil
	}

	binding := "sole candidate"
	if explicitUsed {
		binding = "explicit bind"
	}
	switch {
	case len(own) == 1:
		return selection{provider: own[0], binding: binding}
	case len(own) > 1:
		return selection{requested: effective, requestedType: effectiveType, candidates: own, explicit: explicitUsed}
	case len(foreign) == 1:
		return selection{provider: foreign[0], binding: binding}
	case explicitUsed:
		// A Bind names a concrete type, so there is no structural search
		// to fall back on.
		sel := selection{requested: effective, requestedType: effectiveType, unusable: unusable, explicit: true}
		if len(foreign) > 1 {
			sel.candidates, sel.foreign = foreign, true
		}
		return sel
	}

	iface, ok := interfaceOf(kType)
	if !ok {
		sel := selection{requested: k, requestedType: kType, unusable: unusable}
		if len(foreign) > 1 {
			sel.candidates, sel.foreign = foreign, true
		}
		return sel
	}

	var structural []*graph.Provider
	for _, c := range r.all {
		if !r.scope[c.Pkg] || r.reach.providerProblem(c) != "" {
			continue
		}
		if types.Implements(c.ResultType, iface) {
			structural = append(structural, c)
		}
	}
	switch {
	case len(structural) == 1:
		return selection{provider: structural[0], binding: "sole implementation"}
	case len(structural) > 1:
		return selection{requested: k, requestedType: kType, candidates: structural, isInterface: true}
	case len(foreign) > 1:
		return selection{requested: k, requestedType: kType, candidates: foreign, isInterface: true, foreign: true, unusable: unusable}
	default:
		return selection{requested: k, requestedType: kType, isInterface: true, unusable: unusable}
	}
}

func interfaceOf(t types.Type) (*types.Interface, bool) {
	if t == nil {
		return nil, false
	}
	iface, ok := types.Unalias(t).Underlying().(*types.Interface)
	if !ok || iface.NumMethods() == 0 {
		return nil, false
	}
	return iface, true
}
