package resolve

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// NearMiss is something that comes close to providing a requested type and
// does not: a function the scan refused, a provider this injector cannot
// reference, or one that provides a near twin of the type — T for *T, or
// a value whose pointer-receiver methods are what the interface needed.
// "No provider for *cache.Cache", with cache.New sitting right there, is
// the message these exist to end.
type NearMiss struct {
	// Kind is "rejected" (the scan refused the function — the reason
	// `servo list --rejected` gives), "unusable" (a candidate generated
	// code in this injector cannot refer to), "near miss" (it provides a
	// different type than the one asked for), or "note" (no function at
	// all: the type itself can never be provided). A note has no Func or
	// Pos.
	Kind string
	// Func names the function as diagnostics do: "cache.New", or
	// "cache.(*Pool).Get" for a method.
	Func   string
	Pos    token.Position
	Reason string
}

func (m NearMiss) String() string {
	if m.Func == "" {
		return m.Kind + ": " + m.Reason
	}
	return fmt.Sprintf("%s: %s (%s) — %s", m.Kind, m.Func, m.Pos, m.Reason)
}

// NearMisses answers "why is nothing providing want?" for the injector
// in.Spec describes, with the engine a "no provider" diagnostic uses, so
// `servo explain` and `servo why` can say the same thing about a type the
// graph does not contain. It reads in.Spec.InjectorPkg, in.Candidates,
// in.Scope and in.Pkgs, and nothing else; the rest of Input may be zero.
//
// Only the main module's functions are offered as near misses — a
// dependency's refused functions are not the reader's to change — except
// the twins of an exact request, which are named wherever they live:
// "slog.New returns *slog.Logger, not slog.Logger" is the answer to asking
// for the wrong one of the two.
func NearMisses(in Input, want types.Type) []NearMiss {
	injector, importable := injectorOf(in)
	f := newFinder(in.Candidates, in.Scope, in.Pkgs, injector, newReach(injector, importable), nil)
	return f.nearMisses(want)
}

// Unreachable reports every candidate in in.Candidates that generated code
// in the injector in.Spec describes cannot refer to — a provider internal
// to another tree, one whose result type is unexported, one whose package
// imports the injector's — as NearMisses of kind "unusable", in source
// order. They are in the candidate index, which is injector-independent,
// and still never selected for this injector; `servo list --rejected`
// lists them beside the functions the scan refused outright.
func Unreachable(in Input) []NearMiss {
	injector, importable := injectorOf(in)
	rc := newReach(injector, importable)
	var out []NearMiss
	for _, c := range in.Candidates {
		if why := rc.providerProblem(c); why != "" {
			out = append(out, NearMiss{Kind: "unusable", Func: c.Name, Pos: c.Pos, Reason: why})
		}
	}
	var order *graph.SourceOrder
	if in.Pkgs != nil {
		order = graph.NewSourceOrder(in.Pkgs)
	}
	sort.SliceStable(out, func(i, j int) bool { return order.Compare(out[i].Pos, out[j].Pos) < 0 })
	return out
}

// injectorOf reads the injector package's path off in.Spec, and whether
// anything could import it — nothing can import a package main.
func injectorOf(in Input) (path string, importable bool) {
	if in.Spec == nil || in.Spec.InjectorPkg == nil {
		return "", true
	}
	pkg := in.Spec.InjectorPkg
	return pkg.PkgPath, pkg.Types == nil || pkg.Types.Name() != "main"
}

// finder is the near-miss engine over one injector's candidate universe.
// The resolver keeps one for its diagnostics; NearMisses builds one per
// question.
type finder struct {
	all      []*graph.Provider
	byResult map[graph.Key][]*graph.Provider
	scope    map[string]bool
	pkgs     []*packages.Package
	injector string
	reach    *reach
	order    *graph.SourceOrder

	// rejected is every function the main module's packages offer that
	// the scan refused, scanned the first time a diagnostic needs it:
	// near misses are a failure-path question, and a graph that resolves
	// never pays for them.
	rejected []nearCandidate
	scanned  bool
}

// nearCandidate is a refused function, or a package-level variable of
// function type, with the type it would have provided.
type nearCandidate struct {
	name   string
	pos    token.Position
	reason string
	result types.Type
}

func newFinder(all []*graph.Provider, scope map[string]bool, pkgs []*packages.Package, injector string, rc *reach, byResult map[graph.Key][]*graph.Provider) *finder {
	if byResult == nil {
		byResult = make(map[graph.Key][]*graph.Provider, len(all))
		for _, c := range all {
			byResult[c.Result] = append(byResult[c.Result], c)
		}
	}
	var order *graph.SourceOrder
	if pkgs != nil {
		order = graph.NewSourceOrder(pkgs)
	}
	return &finder{all: all, byResult: byResult, scope: scope, pkgs: pkgs, injector: injector, reach: rc, order: order}
}

// nearMisses lists, for want: providers of it this injector cannot use;
// for an interface, implementations it cannot use and values whose
// pointer has the methods; otherwise the providers of its T/*T twin; then
// every refused main-module function that would have provided it or its
// twin; and a note when want is a type no function can ever provide.
// Each group is in source order.
func (f *finder) nearMisses(want types.Type) []NearMiss {
	var out []NearMiss
	if ok, why := graph.ValidResultType(want); !ok {
		out = append(out, NearMiss{Kind: "note", Reason: fmt.Sprintf(
			"%s is never provided: it %s, and only named types, pointers to named types and non-empty interfaces are — declare a named type for it and depend on that, or supply it with servo.Value",
			graph.TypeString(want), strings.TrimPrefix(why, "result type "))})
	}
	key := graph.NewKey(want, "")
	for _, c := range f.sorted(f.byResult[key]) {
		if why := f.reach.providerProblem(c); why != "" {
			out = append(out, NearMiss{Kind: "unusable", Func: c.Name, Pos: c.Pos, Reason: why})
		}
	}

	iface, isIface := interfaceOf(want)
	if isIface {
		for _, c := range f.sorted(f.all) {
			if !f.scope[c.Pkg] {
				continue
			}
			switch {
			case types.Implements(c.ResultType, iface):
				if why := f.reach.providerProblem(c); why != "" && c.Result != key {
					out = append(out, NearMiss{Kind: "unusable", Func: c.Name, Pos: c.Pos, Reason: why})
				}
			case pointerImplements(c.ResultType, iface):
				method, _ := types.MissingMethod(c.ResultType, iface, true)
				out = append(out, NearMiss{Kind: "near miss", Func: c.Name, Pos: c.Pos, Reason: fmt.Sprintf(
					"returns %s, which does not implement %s: %s has a pointer receiver, so only *%s does — return the pointer",
					short(c.ResultType), short(want), method.Name(), short(c.ResultType))})
			}
		}
	} else if twin := twinOf(want); twin != nil {
		for _, c := range f.sorted(f.byResult[graph.NewKey(twin, "")]) {
			out = append(out, NearMiss{Kind: "near miss", Func: c.Name, Pos: c.Pos, Reason: fmt.Sprintf(
				"returns %s, not %s — a type and a pointer to it are two different nodes", short(twin), short(want))})
		}
	}

	twin := twinOf(want)
	for _, rj := range f.refused() {
		if rj.result == nil {
			continue
		}
		switch {
		case graph.Produces(rj.result, want):
			out = append(out, NearMiss{Kind: "rejected", Func: rj.name, Pos: rj.pos, Reason: rj.reason})
		case isIface && !mentionsTypeParam(rj.result) && types.Implements(rj.result, iface):
			out = append(out, NearMiss{Kind: "rejected", Func: rj.name, Pos: rj.pos, Reason: rj.reason})
		case twin != nil && graph.Produces(rj.result, twin):
			out = append(out, NearMiss{Kind: "rejected", Func: rj.name, Pos: rj.pos, Reason: fmt.Sprintf("%s — and it returns %s, not %s", rj.reason, short(twin), short(want))})
		}
	}
	return out
}

// refused scans the main module once for the functions the candidate scan
// refused, plus package-level variables of function type — `var NewClient =
// newClient` reads like a constructor and has never been one.
func (f *finder) refused() []nearCandidate {
	if f.scanned {
		return f.rejected
	}
	f.scanned = true
	var main []*packages.Package
	for _, p := range f.pkgs {
		if f.scope[p.PkgPath] && p.Types != nil {
			main = append(main, p)
		}
	}
	_, rejected := graph.ScanCandidates(main, f.injector)
	for _, rj := range rejected {
		f.rejected = append(f.rejected, nearCandidate{name: rj.Name, pos: rj.Pos, reason: rj.Reason, result: rj.Result})
	}
	for _, p := range main {
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			v, ok := scope.Lookup(name).(*types.Var)
			if !ok {
				continue
			}
			sig, ok := v.Type().Underlying().(*types.Signature)
			if !ok || sig.Results().Len() == 0 {
				continue
			}
			f.rejected = append(f.rejected, nearCandidate{
				name:   p.Types.Name() + "." + name,
				pos:    p.Fset.Position(v.Pos()),
				reason: "a package-level variable of function type, not a function — only a top-level func declaration provides",
				result: sig.Results().At(0).Type(),
			})
		}
	}
	sort.SliceStable(f.rejected, func(i, j int) bool { return f.order.Compare(f.rejected[i].pos, f.rejected[j].pos) < 0 })
	return f.rejected
}

func (f *finder) sorted(cands []*graph.Provider) []*graph.Provider {
	out := append([]*graph.Provider(nil), cands...)
	sort.SliceStable(out, func(i, j int) bool { return f.order.Compare(out[i].Pos, out[j].Pos) < 0 })
	return out
}

// twinOf is *T for a named T, and T for a pointer to a named T: the one
// other type a reader who wrote either probably meant. nil for anything
// else.
func twinOf(t types.Type) types.Type {
	switch u := types.Unalias(t).(type) {
	case *types.Pointer:
		if _, ok := types.Unalias(u.Elem()).(*types.Named); ok {
			return u.Elem()
		}
	case *types.Named:
		if _, isIface := u.Underlying().(*types.Interface); !isIface {
			return types.NewPointer(t)
		}
	}
	return nil
}

// short renders a type by package name, *memory.Store rather than
// *example.com/app/memory.Store: a near-miss line already names the
// function and its position, and the full path would push the reason off
// the screen.
func short(t types.Type) string {
	return graph.TypeStringWith(t, packageNameQualifier)
}

// mentionsTypeParam reports whether t is written in terms of a generic
// function's own type parameters — a refused generic constructor's result —
// which types.Implements makes no promises about.
func mentionsTypeParam(t types.Type) bool {
	switch u := types.Unalias(t).(type) {
	case *types.TypeParam:
		return true
	case *types.Pointer:
		return mentionsTypeParam(u.Elem())
	case *types.Slice:
		return mentionsTypeParam(u.Elem())
	case *types.Map:
		return mentionsTypeParam(u.Key()) || mentionsTypeParam(u.Elem())
	case *types.Named:
		for i := range u.TypeArgs().Len() {
			if mentionsTypeParam(u.TypeArgs().At(i)) {
				return true
			}
		}
	}
	return false
}

// pointerImplements reports whether t falls short of iface only because its
// methods have pointer receivers: t is not a pointer, and *t implements it.
func pointerImplements(t types.Type, iface *types.Interface) bool {
	if _, isPtr := types.Unalias(t).(*types.Pointer); isPtr {
		return false
	}
	return types.Implements(types.NewPointer(t), iface)
}
