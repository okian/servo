package emit

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v3/internal/resolve"
)

// newFunc emits the constructor: sequential DFS-post-order construction
// with statically unrolled rollback, then the Init phase.
func (e *emitter) newFunc() string {
	var b strings.Builder
	b.WriteString(e.zeroValueDelegate())
	if len(e.values) > 0 {
		fmt.Fprintf(&b, "func %s(ctx context.Context, v %s) (*%s, error) {\n", e.withConstructorName(), e.valuesType(), e.appType())
	} else {
		fmt.Fprintf(&b, "func %s(ctx context.Context) (*%s, error) {\n", e.constructorName(), e.appType())
	}
	fmt.Fprintf(&b, "\ta := &%s{}\n\n", e.appType())
	b.WriteString(e.valueAssignments())
	b.WriteString(e.configAssignments())
	b.WriteString(e.scopeSetup())

	for i, n := range e.resolved.Order {
		e.writeConstruction(&b, i, n)
	}

	b.WriteString(e.initPhase())

	b.WriteString("\treturn a, nil\n")
	b.WriteString("}\n\n")
	return b.String()
}

func (e *emitter) writeConstruction(b *strings.Builder, index int, n *resolve.Node) {
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
	b.WriteString("\n")
}

// writeConstructionRollback stops nodes 0..index-1 in reverse — the set
// known to have been successfully constructed before this failure — by
// literal, unrolled calls: no runtime bookkeeping of "what succeeded so
// far" is needed. The App is only partially populated at this point, so
// this cannot simply defer to Shutdown as the Init-phase rollback does.
func (e *emitter) writeConstructionRollback(b *strings.Builder, index int) {
	for i := index - 1; i >= 0; i-- {
		n := e.resolved.Order[i]
		if !e.needsStopMethod(n) {
			continue
		}
		fmt.Fprintf(b, "\t\t_ = a.stop%s(%s)\n", capitalize(e.varName[n.Key]), rollbackCtx)
	}
}

// rollbackCtx is the context every rollback path hands the stop calls.
//
// New's own ctx is almost always the signal context — every main in the
// docs and the examples passes it — so a SIGTERM arriving mid-startup
// cancels it, aborts an Init, and then the unwind that follows would be
// handed the same, already-cancelled context. servo.RunStop derives its
// budget from it, so Done is closed before the select runs and every node
// is reported abandoned without its Drain, Flush or Stop ever getting a
// chance to do anything. Stripping the cancellation is the same rule
// lifecycle.md states for a hand-written main's Shutdown, and the same one
// scoped teardown already follows; RunStop still caps each phase, so
// nothing here can hang.
const rollbackCtx = "context.WithoutCancel(ctx)"

// initPhase calls Init in topological order, one errgroup per concurrency
// level, rolling back via the now-fully-constructed App's own Shutdown on
// failure — safe here because every node exists by the time Init begins,
// unlike the construction phase above.
func (e *emitter) initPhase() string {
	groups := levelGroups(e.resolved.Order, func(n *resolve.Node) bool { return hasCapability(n, "Initializer") })
	var b strings.Builder
	for _, level := range groups {
		if len(level) == 1 {
			e.writeSingleInit(&b, level[0])
		} else {
			e.writeConcurrentInit(&b, level)
		}
	}
	return b.String()
}

func (e *emitter) writeSingleInit(b *strings.Builder, n *resolve.Node) {
	e.imports.Add("time", "time")
	e.imports.Add("errors", "errors")
	name := e.varName[n.Key]
	b.WriteString("\t{\n\t\tstart := time.Now()\n")
	fmt.Fprintf(b, "\t\terr := a.%s.Init(ctx)\n", name)
	fmt.Fprintf(b, "\t\ta.startupReport.Nodes = append(a.startupReport.Nodes, %s.StartupNode{Type: %q, Duration: time.Since(start)})\n", e.servoAlias, n.Key.String())
	b.WriteString("\t\tif err != nil {\n")
	b.WriteString(e.rollbackReturn())
	b.WriteString("\t\t}\n\t}\n")
}

// rollbackReturn is the shared tail of both Init-failure paths: unwind,
// then return the failure with the unwind's report attached only when the
// unwind had something to say.
//
// Report satisfies error by value, so it is never nil and errors.Join
// never skips it — and a clean report's Error() is the empty string, which
// errors.Join still separates with a newline. Joining unconditionally
// therefore appended a blank line to every ordinary startup failure, which
// survives into any log field or %w wrapping built from it.
func (e *emitter) rollbackReturn() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\t\t\treport := a.Shutdown(%s)\n", rollbackCtx)
	b.WriteString("\t\t\tif report.Clean() {\n")
	b.WriteString("\t\t\t\treturn nil, err\n")
	b.WriteString("\t\t\t}\n")
	b.WriteString("\t\t\treturn nil, errors.Join(err, report)\n")
	return b.String()
}

func (e *emitter) writeConcurrentInit(b *strings.Builder, nodes []*resolve.Node) {
	e.imports.Add("time", "time")
	e.imports.Add("errors", "errors")
	e.imports.Add("sync", "sync")
	e.imports.Add("golang.org/x/sync/errgroup", "errgroup")

	b.WriteString("\t{\n\t\tvar timingMu sync.Mutex\n\t\tg, gctx := errgroup.WithContext(ctx)\n")
	for _, n := range nodes {
		name := e.varName[n.Key]
		b.WriteString("\t\tg.Go(func() error {\n\t\t\tstart := time.Now()\n")
		fmt.Fprintf(b, "\t\t\terr := a.%s.Init(gctx)\n", name)
		b.WriteString("\t\t\ttimingMu.Lock()\n")
		fmt.Fprintf(b, "\t\t\ta.startupReport.Nodes = append(a.startupReport.Nodes, %s.StartupNode{Type: %q, Duration: time.Since(start)})\n", e.servoAlias, n.Key.String())
		b.WriteString("\t\t\ttimingMu.Unlock()\n\t\t\treturn err\n\t\t})\n")
	}
	b.WriteString("\t\tif err := g.Wait(); err != nil {\n")
	b.WriteString(e.rollbackReturn())
	b.WriteString("\t\t}\n\t}\n")
}

// appArg renders one constructor argument inside New. Everything the App
// builds itself is still in scope as a local; a scope accessor is not
// built by a provider at all, so it comes off the App, where scopeSetup
// put it before any of this ran.
func (e *emitter) appArg(dep *resolve.Node) string {
	if dep.Kind == resolve.NodeScopeAccessor {
		return "a." + e.rootByAccessor[dep.ScopeRoot].AccessorField
	}
	return e.varName[dep.Key]
}
