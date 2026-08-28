package load

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// ScopeDecl is one servo.Scoped[T, I](...) declaration: the scoped type,
// the user-declared accessor interface the generated accessor satisfies,
// and the two policy knobs.
//
// Linger and Max are recorded as "set or not" rather than folded into the
// defaults here, so a second declaration sharing the same key type can be
// checked for a *conflicting* value rather than for a merely-absent one.
type ScopeDecl struct {
	Impl      graph.Key
	ImplType  types.Type
	Iface     graph.Key
	IfaceType types.Type

	Linger    time.Duration
	LingerSet bool
	Max       int
	MaxSet    bool

	Pos token.Position
}

// EffectiveLinger and EffectiveMax apply the package defaults for a
// declaration that omitted the option.
func (d ScopeDecl) EffectiveLinger() time.Duration {
	if d.LingerSet {
		return d.Linger
	}
	return defaultLinger
}

func (d ScopeDecl) EffectiveMax() int {
	if d.MaxSet {
		return d.Max
	}
	return defaultMax
}

// The generator bakes these into a scope whose declaration omits the
// option. They mirror servo.DefaultLinger/servo.DefaultMax, duplicated
// rather than imported so internal/load stays free of a dependency on the
// runtime package it is reading the *syntax* of — and pinned to those
// constants by a test.
const (
	defaultLinger = 30 * time.Second
	defaultMax    = 10_000
)

// parseScopedCall reads one servo.Scoped[T, I](opts...) argument.
func parseScopedCall(pkg *packages.Package, call *ast.CallExpr, typeArgs []types.Type, pos token.Position) (ScopeDecl, error) {
	if len(typeArgs) != 2 {
		return ScopeDecl{}, fmt.Errorf("%s: servo.Scoped expects exactly two type arguments", pos)
	}
	impl, iface := typeArgs[0], typeArgs[1]

	if _, isPtr := types.Unalias(impl).(*types.Pointer); !isPtr && !isInterfaceType(impl) {
		return ScopeDecl{}, fmt.Errorf("%s: servo.Scoped's first type argument must be a pointer, not %s — Acquire reports failure by returning a nil instance alongside the error, and a value type has no nil to return; declare servo.Scoped[*%s, %s]",
			pos, graph.TypeString(impl), graph.TypeString(impl), graph.TypeString(iface))
	}
	if isInterfaceType(impl) {
		return ScopeDecl{}, fmt.Errorf("%s: servo.Scoped's first type argument must be the concrete scoped type, not an interface (%s) — it is the type whose %s method identifies the scope",
			pos, graph.TypeString(impl), graph.ScopeKeyMethodName)
	}
	ifaceUnder, ok := types.Unalias(iface).Underlying().(*types.Interface)
	if !ok {
		return ScopeDecl{}, fmt.Errorf("%s: servo.Scoped's second type argument must be an interface (%s is not) — it is the accessor type your own package declares, which the generated accessor satisfies:\n\n\ttype Rooms interface {\n\t    Acquire(ctx context.Context) (%s, func(), error)\n\t}",
			pos, graph.TypeString(iface), localTypeString(impl))
	}
	if ifaceUnder.NumMethods() == 0 {
		return ScopeDecl{}, fmt.Errorf("%s: servo.Scoped's accessor interface %s declares no methods — an empty interface is satisfied by everything, which would make the accessor unusable as a dependency", pos, graph.TypeString(iface))
	}

	decl := ScopeDecl{
		Impl: graph.NewKey(impl, ""), ImplType: impl,
		Iface: graph.NewKey(iface, ""), IfaceType: iface,
		Pos: pos,
	}
	if err := parseScopeOptions(pkg, call, &decl); err != nil {
		return ScopeDecl{}, err
	}
	return decl, nil
}

// parseScopeOptions reads the servo.Linger/servo.Max calls inside a
// Scoped(...) argument list. Like every other part of the spec they are
// read as syntax: the argument has to be a constant expression, because
// nothing here is ever evaluated.
func parseScopeOptions(pkg *packages.Package, call *ast.CallExpr, decl *ScopeDecl) error {
	for _, arg := range call.Args {
		optCall, ok := arg.(*ast.CallExpr)
		if !ok {
			return notAScopeOption(pkg.Fset.Position(arg.Pos()))
		}
		// markerCall resolves plain and generic call shapes alike, so a
		// misplaced servo.Root[T]() inside Scoped(...) is named in the
		// diagnostic instead of falling into the generic "not a marker".
		name, _, optPos, err := markerCall(pkg, optCall)
		if err != nil {
			return notAScopeOption(pkg.Fset.Position(arg.Pos()))
		}

		switch name {
		case "Linger":
			if len(optCall.Args) != 1 {
				return fmt.Errorf("%s: servo.Linger expects exactly one argument", optPos)
			}
			d, err := constantDuration(pkg, optCall.Args[0], optPos)
			if err != nil {
				return err
			}
			if decl.LingerSet {
				return fmt.Errorf("%s: servo.Linger declared twice in the same servo.Scoped", optPos)
			}
			decl.Linger, decl.LingerSet = d, true
		case "Max":
			if len(optCall.Args) != 1 {
				return fmt.Errorf("%s: servo.Max expects exactly one argument", optPos)
			}
			n, err := constantInt(pkg, optCall.Args[0], optPos)
			if err != nil {
				return err
			}
			if n <= 0 {
				return fmt.Errorf("%s: servo.Max(%d) must be positive — a scope that can hold no instances can never hand one out", optPos, n)
			}
			if decl.MaxSet {
				return fmt.Errorf("%s: servo.Max declared twice in the same servo.Scoped", optPos)
			}
			decl.Max, decl.MaxSet = n, true
		default:
			return fmt.Errorf("%s: servo.%s is not a scope option — servo.Scoped accepts servo.Linger(...) and servo.Max(...)", optPos, name)
		}
	}
	return nil
}

func notAScopeOption(pos token.Position) error {
	return fmt.Errorf("%s: servo.Scoped's arguments must be servo.Linger(...) or servo.Max(...) calls", pos)
}

func constantDuration(pkg *packages.Package, expr ast.Expr, pos token.Position) (time.Duration, error) {
	v, err := constantValue(pkg, expr, pos, "servo.Linger")
	if err != nil {
		return 0, err
	}
	n, ok := constant.Int64Val(constant.ToInt(v))
	if !ok {
		return 0, fmt.Errorf("%s: servo.Linger's argument must be a constant time.Duration, not %s", pos, v.String())
	}
	if n < 0 {
		return 0, fmt.Errorf("%s: servo.Linger(%s) must not be negative — use servo.Linger(0) for die-with-the-last-holder", pos, time.Duration(n))
	}
	return time.Duration(n), nil
}

func constantInt(pkg *packages.Package, expr ast.Expr, pos token.Position) (int, error) {
	v, err := constantValue(pkg, expr, pos, "servo.Max")
	if err != nil {
		return 0, err
	}
	n, ok := constant.Int64Val(constant.ToInt(v))
	if !ok {
		return 0, fmt.Errorf("%s: servo.Max's argument must be a constant integer, not %s", pos, v.String())
	}
	return int(n), nil
}

// constantValue folds expr with go/types' own constant evaluation — the
// same reason Root/Bind read type arguments through TypesInfo rather than
// re-implementing them: the spec file is type-checked, so the answer is
// already computed and correct for named constants, arithmetic, and
// untyped literals alike.
func constantValue(pkg *packages.Package, expr ast.Expr, pos token.Position, what string) (constant.Value, error) {
	tv, ok := pkg.TypesInfo.Types[expr]
	if !ok || tv.Value == nil {
		return nil, fmt.Errorf("%s: %s's argument must be a constant expression — the spec file is read as syntax and never executed, so a variable or function call has no value to read", pos, what)
	}
	return tv.Value, nil
}

// checkScopeDecls rejects the two ways a set of Scoped declarations can
// collide with itself, both of which would otherwise surface much later as
// a confusing generated-code compile error.
func checkScopeDecls(decls []ScopeDecl) error {
	byImpl := map[graph.Key]ScopeDecl{}
	byIface := map[graph.Key]ScopeDecl{}
	for _, d := range decls {
		if prior, dup := byImpl[d.Impl]; dup {
			return fmt.Errorf("%s: servo.Scoped[%s, ...] declared twice — first at %s", d.Pos, d.Impl.String(), prior.Pos)
		}
		byImpl[d.Impl] = d
		if prior, dup := byIface[d.Iface]; dup {
			return fmt.Errorf("%s: servo.Scoped[..., %s] declared twice, for %s and %s — one accessor interface cannot stand for two scoped types",
				d.Pos, d.Iface.String(), prior.Impl.String(), d.Impl.String())
		}
		byIface[d.Iface] = d
	}
	return nil
}

// localTypeString renders t the way it is written inside its own package —
// "*Room", not "*example.com/chat.Room" — for a snippet meant to be pasted
// into that package. An instantiated generic keeps its type arguments.
func localTypeString(t types.Type) string {
	if ptr, isPtr := types.Unalias(t).(*types.Pointer); isPtr {
		return "*" + localTypeString(ptr.Elem())
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return graph.TypeString(t)
	}
	name := named.Obj().Name()
	if targs := named.TypeArgs(); targs != nil && targs.Len() > 0 {
		args := make([]string, targs.Len())
		for i := range args {
			args[i] = localTypeString(targs.At(i))
		}
		name += "[" + strings.Join(args, ", ") + "]"
	}
	return name
}
