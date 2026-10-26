package emit

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v3/internal/resolve"
)

// newFunc emits the constructor: sequential DFS-post-order construction,
// each failure unwinding what was built before it, then the emitted HTTP
// servers (wiring only), then the Init phase.
func (e *emitter) newFunc() string {
	var b strings.Builder
	b.WriteString(e.zeroValueDelegate())
	if e.takesValues() {
		fmt.Fprintf(&b, "func %s(ctx context.Context, v %s) (*%s, error) {\n", e.withConstructorName(), e.valuesType(), e.appType())
	} else {
		fmt.Fprintf(&b, "func %s(ctx context.Context) (*%s, error) {\n", e.constructorName(), e.appType())
	}
	b.WriteString(e.valueChecks())
	fmt.Fprintf(&b, "\ta := &%s{}\n\n", e.appType())
	b.WriteString(e.valueAssignments())
	b.WriteString(e.configAssignments())
	b.WriteString(e.scopeSetup())
	if e.undoVar != "" {
		fmt.Fprintf(&b, "\tvar %s []func(context.Context) %s.NodeResult\n\n", e.undoVar, e.servoAlias)
	}

	for i, n := range e.resolved.Order {
		e.writeConstruction(&b, i, n)
	}

	b.WriteString(e.httpSetup())
	b.WriteString(e.initPhase())

	b.WriteString("\treturn a, nil\n")
	b.WriteString("}\n\n")
	return b.String()
}

func (e *emitter) writeConstruction(b *strings.Builder, index int, n *resolve.Node) {
	if n.Kind == resolve.NodeTraced {
		e.writeTracedConstruction(b, n)
		return
	}
	if f := e.overrideByNode[n]; f != nil {
		e.writeOverrideConstruction(b, index, f)
		return
	}
	name := e.varName[n.Key]
	args := make([]string, len(n.Deps))
	for i, dep := range n.Deps {
		args[i] = e.appArg(dep)
	}
	call := fmt.Sprintf("%s(%s)", e.qualifiedFuncString(n.Provider), strings.Join(args, ", "))

	switch {
	case n.Provider.HasCleanup && n.Provider.HasError:
		fmt.Fprintf(b, "\t%s, %sCleanup, err := %s\n", name, name, call)
	case n.Provider.HasCleanup:
		fmt.Fprintf(b, "\t%s, %sCleanup := %s\n", name, name, call)
	case n.Provider.HasError:
		fmt.Fprintf(b, "\t%s, err := %s\n", name, call)
	default:
		fmt.Fprintf(b, "\t%s := %s\n", name, call)
	}

	if n.Provider.HasError {
		b.WriteString("\tif err != nil {\n")
		e.writeConstructionRollback(b, index)
		b.WriteString("\t\treturn nil, err\n")
		b.WriteString("\t}\n")
	}

	fmt.Fprintf(b, "\ta.%s = %s\n", name, name)
	if n.Provider.HasCleanup {
		fmt.Fprintf(b, "\ta.%sCleanup = %sCleanup\n", name, name)
	}
	if e.undoVar != "" && e.needsStopMethod(n) {
		fmt.Fprintf(b, "\t%s = append(%s, a.stop%s)\n", e.undoVar, e.undoVar, capitalize(name))
	}
	b.WriteString("\n")
}

// writeConstructionRollback unwinds a constructor failure at position
// index: every node built before it that has something to stop is stopped,
// newest first, and only those, because the ones after it don't exist yet.
// The App is only partially populated at this point, so this cannot defer
// to Shutdown as the Init-phase rollback does.
//
// Each stoppable node appends its own stop method to New's undo list right
// after it is built, and each failure site makes one call that runs the
// list backwards. The stops used to be unrolled at every failure site
// instead — one for each stoppable node built before it — which made the
// file quadratic in the graph: five hundred constructors that can all fail
// and all stop came to 136,000 lines, and generate, which compiles what it
// writes, took ten minutes and 4 GB over them. Each site also repeated
// what came before it, so two branches that each added a node could merge
// cleanly into a file whose later sites missed the other branch's stop
// call. With the list, a node's block mentions only that node.
func (e *emitter) writeConstructionRollback(b *strings.Builder, index int) {
	if e.undoVar == "" || index <= e.firstStoppable {
		return
	}
	fmt.Fprintf(b, "\t\ta.%s(%s, %s)\n", e.unwindMethod, rollbackCtx, e.undoVar)
}

// planConstructionRollback names New's undo list and the App method that
// runs it, when some constructor that can fail runs after a node that has
// something to stop, and leaves both unnamed — and unemitted — otherwise:
// a graph whose constructors cannot fail, or fail only before anything
// stoppable exists, has nothing to unwind. Named after every node, so a
// node's own name never moves to make room for them.
func (e *emitter) planConstructionRollback() {
	e.firstStoppable = -1
	needed := false
	for i, n := range e.resolved.Order {
		if e.firstStoppable < 0 && e.needsStopMethod(n) {
			e.firstStoppable = i
		}
		if e.firstStoppable >= 0 && i > e.firstStoppable && fallible(n) {
			needed = true
			break
		}
	}
	if !needed {
		return
	}
	// The list is a local in New's one flat scope, the scope every package
	// a later constructor is qualified with is written in too. Reserved
	// before any user package is registered, so a package named undo is
	// aliased instead of hidden behind the slice.
	e.undoVar = e.names.AllocateName("undo")
	e.imports.Reserve(e.undoVar)
	e.unwindMethod = e.names.AllocateName("unwind")
}

// fallible reports whether n's constructor can fail. A wrapper's cannot:
// it has no provider, only a generated constructor that returns the value.
func fallible(n *resolve.Node) bool {
	return n.Kind != resolve.NodeTraced && n.Provider.HasError
}

// unwindMethodDecl emits the App method every construction failure calls,
// or nothing when planConstructionRollback found nothing to unwind. Stop
// results are discarded, as they always have been: the error New returns
// is the constructor's own, and a report about a half-built graph would
// only bury it.
func (e *emitter) unwindMethodDecl() string {
	if e.unwindMethod == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %s stops, newest first, every node built before a constructor failed:\n", e.unwindMethod)
	b.WriteString("// undo holds their stop methods in the order the nodes were built.\n")
	fmt.Fprintf(&b, "func (a *%s) %s(ctx context.Context, undo []func(context.Context) %s.NodeResult) {\n", e.appType(), e.unwindMethod, e.servoAlias)
	b.WriteString("\tfor i := len(undo) - 1; i >= 0; i-- {\n\t\t_ = undo[i](ctx)\n\t}\n}\n\n")
	return b.String()
}

// rollbackCtx is the context every rollback path hands the stop calls.
//
// New's own ctx is almost always the signal context — every main in the
// docs and the examples passes it — so a SIGTERM arriving mid-startup
// cancels it, aborts an Init, and then the unwind that follows would be
// handed the same, already-cancelled context. servoimpl.RunStop derives its
// budget from it, so Done is closed before the select runs and every node
// is reported abandoned without its Drain, Flush or Stop ever getting a
// chance to do anything. Stripping the cancellation is the same rule
// lifecycle.md states for a hand-written main's Shutdown, and the same one
// scoped teardown already follows; RunStop still caps each phase, so
// nothing here can hang.
const rollbackCtx = "context.WithoutCancel(ctx)"

// initPhase emits the whole Init phase as one servoimpl.Init call — one
// slice of calls per level, in level order — and the rollback that follows
// a failure, through the now-fully-constructed App's own Shutdown: safe
// here because every node exists by the time Init begins, unlike the
// construction phase above.
//
// One helper runs every level, a lone node's included. The generated code
// used to call a lone Init directly, on New's own long-lived context, and
// give a level of several an errgroup, whose context dies when Wait
// returns: a refresher an Init started on its context outlived New alone
// and died the moment an unrelated Initializer joined its level. A panic
// was as uneven — a lone one unwound into main, a shared one killed the
// process from the errgroup's goroutine — and neither ran the rollback.
// The helper gives every call the same call-scoped context, recovers
// panics into that node's failure, names the node in every error, and
// fails the phase when New's context ended under it.
func (e *emitter) initPhase() string {
	groups := levelGroups(e.resolved.Order, func(n *resolve.Node) bool { return hasCapability(n, "Initializer") })
	if len(groups) == 0 {
		return ""
	}
	impl := e.runtimeImpl()
	var b strings.Builder
	fmt.Fprintf(&b, "\tif err := %s.Init(ctx, %s.DefaultInitBudget, &a.startupReport,\n", impl, e.servoAlias)
	for _, level := range groups {
		fmt.Fprintf(&b, "\t\t[]%s.Call{\n", impl)
		for _, n := range level {
			fmt.Fprintf(&b, "\t\t\t{Node: %q, Fn: a.%s.Init},\n", n.Key.String(), e.varName[n.Key])
		}
		b.WriteString("\t\t},\n")
	}
	b.WriteString("\t); err != nil {\n")
	b.WriteString(e.rollbackReturn())
	b.WriteString("\t}\n")
	return b.String()
}

// rollbackReturn is the shared tail of every failure New unwinds through
// Shutdown — the Init phase's, and the emitted servers' setup: unwind,
// then return the failure, carrying the unwind's report only when the
// unwind had something to say.
//
// servoimpl.RolledBack decides both. It returns err itself after a clean
// unwind: Report satisfies error by value, so a joined clean report used
// to add its empty Error() as a trailing blank line to every ordinary
// startup failure. And it attaches an unclean one where errors.As finds
// it but errors.Is does not look: joined, a rollback Stop that overran its
// budget made errors.Is(err, context.DeadlineExceeded) true for a startup
// that failed for a different reason entirely.
func (e *emitter) rollbackReturn() string {
	return fmt.Sprintf("\t\treturn nil, %s.RolledBack(err, a.Shutdown(%s))\n", e.runtimeImpl(), rollbackCtx)
}

// appArg renders one constructor argument inside New. Everything the App
// builds itself is still in scope as a local; a scope accessor is not
// built by a provider at all, so it comes off the App, where scopeSetup
// put it before any of this ran; and the construction context is New's own
// ctx parameter — the context the caller handed New, NewWith or NewTestApp.
func (e *emitter) appArg(dep *resolve.Node) string {
	if dep.Kind == resolve.NodeScopeAccessor {
		return "a." + e.rootByAccessor[dep.ScopeRoot].AccessorField
	}
	if dep.Kind == resolve.NodeContext {
		return "ctx"
	}
	return e.varName[dep.Key]
}
