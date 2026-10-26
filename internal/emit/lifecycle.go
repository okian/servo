package emit

import (
	"fmt"
	"path"
	"strings"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/resolve"
)

// runtimeImpl registers the servoimpl import on first use and returns the
// identifier generated code reaches it by. Every helper the lifecycle and
// scope code calls lives there rather than in package servo: generated
// code is in the user's package, so whatever it calls has to be exported,
// and exporting it from servo froze it as public API — RunStop,
// MergeNodeResults and LingerWindow are still there for that reason. Add
// is idempotent, so the alias is the same however many emitters ask, and
// whichever of them asks first.
func (e *emitter) runtimeImpl() string {
	return e.imports.Add(path.Join(path.Dir(graph.ServoPackagePath), "servoimpl"), "servoimpl")
}

// stopMethods emits one private stop method per node that has anything to
// stop: Drain, then Flush, then Stop, then the provider's own cleanup func.
// There is no separate quiesce phase — waiting for in-flight work is the
// component's own responsibility inside Drain. sync.Once makes each stop
// method safe to call from both construction rollback and Shutdown.
func (e *emitter) stopMethods() string {
	var b strings.Builder
	for _, n := range e.resolved.Order {
		if !e.needsStopMethod(n) {
			continue
		}
		e.writeStopMethod(&b, n)
	}
	return b.String()
}

func (e *emitter) writeStopMethod(b *strings.Builder, n *resolve.Node) {
	name := e.varName[n.Key]
	label := n.Key.String()
	impl := e.runtimeImpl()

	fmt.Fprintf(b, "func (a *%s) stop%s(ctx context.Context) %s.NodeResult {\n", e.appType(), capitalize(name), e.servoAlias)
	fmt.Fprintf(b, "\ta.%sStopOnce.Do(func() {\n", name)
	fmt.Fprintf(b, "\t\tvar results []%s.NodeResult\n", e.servoAlias)

	if hasCapability(n, "Drainer") {
		fmt.Fprintf(b, "\t\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, a.%s.Drain))\n", impl, e.servoAlias, label, name)
	}
	if hasCapability(n, "Flusher") {
		fmt.Fprintf(b, "\t\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, a.%s.Flush))\n", impl, e.servoAlias, label, name)
	}
	if hasCapability(n, "Finalizer") {
		fmt.Fprintf(b, "\t\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, a.%s.Stop))\n", impl, e.servoAlias, label, name)
	}
	if n.Provider.HasCleanup {
		// Guarded, because a constructor with nothing to clean up on some
		// path returning a nil func is ordinary Go, and (T, func()) is a
		// documented provider shape. Unguarded, that nil call panicked
		// inside RunStop's own goroutine, where no recover in main could
		// reach it. Skipping the phase rather than recording an OK result
		// for it leaves the merged NodeResult identical either way.
		fmt.Fprintf(b, "\t\tif a.%sCleanup != nil {\n", name)
		fmt.Fprintf(b, "\t\t\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, func(context.Context) error { a.%sCleanup(); return nil }))\n", impl, e.servoAlias, label, name)
		b.WriteString("\t\t}\n")
	}

	fmt.Fprintf(b, "\t\ta.%sStopResult = %s.MergeNodeResults(%q, results...)\n", name, impl, label)
	b.WriteString("\t})\n")
	fmt.Fprintf(b, "\treturn a.%sStopResult\n", name)
	b.WriteString("}\n\n")
}

// runFunc hands every Runner, and every emitted group server, to one
// servoimpl.Run call, which supervises them as the errgroup used to — one
// failing runner cancels the rest, and Run waits for them — with what the
// errgroup never had: a recover around each runner, a bounded wait once the
// context is done, and failures that name their node.
//
// A runner's goroutine is not main's, so a panic there used to end the
// process on the spot: Shutdown never ran, nothing was drained or
// flushed, and buffered data was lost. Now it is one failed runner, the
// rest are cancelled, and Run returns it for main's Shutdown to follow.
// A lone runner goes through the same call: it used to be called directly
// to spare an errgroup of one, which left it the only runner with neither
// the recover nor the bounded wait.
func (e *emitter) runFunc() string {
	runners := []*resolve.Node{}
	for _, n := range e.resolved.Order {
		if hasCapability(n, "Runner") {
			runners = append(runners, n)
		}
	}

	// The generated group servers are more runners — their run methods are
	// emitted directly rather than detected, but they are supervised
	// alongside the rest, so one failing runner still cancels them all.
	var servers []*httpGroupEmit
	if e.http != nil {
		servers = e.http.Groups
	}

	var b strings.Builder
	fmt.Fprintf(&b, "func (a *%s) Run(ctx context.Context) error {\n", e.appType())
	if len(runners)+len(servers) == 0 {
		b.WriteString("\treturn nil\n")
	} else {
		impl := e.runtimeImpl()
		fmt.Fprintf(&b, "\treturn %s.Run(ctx, %s.DefaultRunGrace,\n", impl, e.servoAlias)
		for _, n := range runners {
			fmt.Fprintf(&b, "\t\t%s.Call{Node: %q, Fn: a.%s.Run},\n", impl, n.Key.String(), e.varName[n.Key])
		}
		for _, g := range servers {
			fmt.Fprintf(&b, "\t\t%s.Call{Node: %q, Fn: a.%s.run},\n", impl, g.stopName(), g.Field)
		}
		b.WriteString("\t)\n")
	}
	b.WriteString("}\n\n")
	return b.String()
}

// shutdownFunc stops every stoppable node in reverse topological order,
// accumulating a Report. A second interrupt/term signal received while
// shutdown is in progress force-exits immediately; signal.NotifyContext's
// own cancellation (used for the *first* signal) lives in the caller's
// main(), so this watcher is only the "second signal" half — handled
// entirely inside generated code so main() itself needs no extra logic.
// servoimpl.ForceExit says so on stderr and exits the way a shell reports
// a process killed by the signal, 128 plus its number; it used to be a
// silent os.Exit(1), indistinguishable from any other failure.
func (e *emitter) shutdownFunc() string {
	e.imports.Add("os", "os")
	e.imports.Add("os/signal", "signal")
	e.imports.Add("syscall", "syscall")

	var b strings.Builder
	fmt.Fprintf(&b, "func (a *%s) Shutdown(ctx context.Context) %s.Report {\n", e.appType(), e.servoAlias)
	b.WriteString("\tforceExit := make(chan os.Signal, 1)\n")
	b.WriteString("\tsignal.Notify(forceExit, os.Interrupt, syscall.SIGTERM)\n")
	b.WriteString("\tdefer signal.Stop(forceExit)\n")
	// signal.Stop only stops FUTURE deliveries — it does not unblock a
	// goroutine already parked on <-forceExit, so the watcher would leak
	// on every clean shutdown without watcherDone to select on too.
	b.WriteString("\twatcherDone := make(chan struct{})\n")
	b.WriteString("\tdefer close(watcherDone)\n")
	fmt.Fprintf(&b, "\tgo func() {\n\t\tselect {\n\t\tcase sig := <-forceExit:\n\t\t\t%s.ForceExit(sig)\n\t\tcase <-watcherDone:\n\t\t}\n\t}()\n\n", e.runtimeImpl())

	fmt.Fprintf(&b, "\tvar nodes []%s.NodeResult\n", e.servoAlias)
	// The servers stop first, before every node: they are the inbound
	// edges, and draining them is what lets everything beneath quiesce —
	// the same reason a root leads the reverse topological order.
	if e.http != nil {
		for _, g := range e.http.Groups {
			fmt.Fprintf(&b, "\tnodes = append(nodes, a.%s(ctx))\n", g.StopMethod)
		}
	}
	for _, step := range e.shutdownSteps() {
		switch {
		case step.scope != nil:
			fmt.Fprintf(&b, "\tnodes = append(nodes, a.%s(ctx))\n", step.scope.StopMethod)
		case e.needsStopMethod(step.node):
			fmt.Fprintf(&b, "\tnodes = append(nodes, a.stop%s(ctx))\n", capitalize(e.varName[step.node.Key]))
		}
	}
	fmt.Fprintf(&b, "\treturn %s.Report{Nodes: nodes}\n", e.servoAlias)
	b.WriteString("}\n\n")
	return b.String()
}

// stopStep is one entry in the teardown sequence: either a singleton node
// or a whole scope.
type stopStep struct {
	node  *resolve.Node
	scope *scopeEmit
}

// shutdownSteps is the teardown order: a reverse topological sort over the
// combined graph of singletons and scopes.
//
// Scopes have to be sequenced, not appended: a scope must stop after every
// singleton that can still call Acquire on it — otherwise Shutdown races
// live traffic — and before every singleton its instances depend on, so a
// draining instance still has a logger and a database to drain against.
// Ordering does most of the work by itself: draining the server first ends
// the streams, which cancels their contexts, which fires the release
// backstops, which drops the reference counts, so by the time a scope is
// reached there is usually very little left in it.
//
// With no scopes declared this is exactly reverse(Order): Order is already
// a DFS post-order, so visiting it in sequence re-derives it element for
// element, and the emitted Shutdown is byte-identical to what it was
// before scopes existed.
func (e *emitter) shutdownSteps() []stopStep {
	visitedNode := map[*resolve.Node]bool{}
	visitedScope := map[*scopeEmit]bool{}
	var out []stopStep

	var visitNode func(n *resolve.Node)
	var visitScope func(se *scopeEmit)

	visitNode = func(n *resolve.Node) {
		// A wrapper is walked through: it has nothing to stop itself, but
		// the implementation behind it does, and a consumer's only path to
		// that implementation runs through the wrapper.
		if n == nil || (n.Kind != resolve.NodeProvider && n.Kind != resolve.NodeTraced) || visitedNode[n] {
			return
		}
		visitedNode[n] = true
		for _, d := range n.Deps {
			if d.Kind == resolve.NodeScopeAccessor {
				visitScope(e.scopeByScope[d.Scope])
				continue
			}
			visitNode(d)
		}
		out = append(out, stopStep{node: n})
	}
	visitScope = func(se *scopeEmit) {
		if se == nil || visitedScope[se] {
			return
		}
		visitedScope[se] = true
		for _, d := range se.Borrowed {
			visitNode(d)
		}
		for _, m := range se.Members {
			for _, d := range m.N.Deps {
				if d.Kind == resolve.NodeScopeAccessor {
					visitScope(e.scopeByScope[d.Scope])
				}
			}
		}
		out = append(out, stopStep{scope: se})
	}

	for _, n := range e.resolved.Order {
		visitNode(n)
	}
	for _, se := range e.scopes {
		visitScope(se)
	}

	reversed := make([]stopStep, len(out))
	for i, step := range out {
		reversed[len(out)-1-i] = step
	}
	return reversed
}

// healthReadyFunc emits Health or Ready: flat per-node results, with no
// transitive aggregation, only for nodes implementing the corresponding
// capability.
func (e *emitter) healthReadyFunc(methodName, capName, callName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "func (a *%s) %s(ctx context.Context) %s.Report {\n", e.appType(), methodName, e.servoAlias)
	fmt.Fprintf(&b, "\tvar nodes []%s.NodeResult\n", e.servoAlias)
	for _, n := range e.resolved.Order {
		if !hasCapability(n, capName) {
			continue
		}
		name := e.varName[n.Key]
		fmt.Fprintf(&b, "\tif err := a.%s.%s(ctx); err != nil {\n", name, callName)
		fmt.Fprintf(&b, "\t\tnodes = append(nodes, %s.NodeResult{Name: %q, Status: %s.StatusFailed, Err: err})\n", e.servoAlias, n.Key.String(), e.servoAlias)
		b.WriteString("\t} else {\n")
		fmt.Fprintf(&b, "\t\tnodes = append(nodes, %s.NodeResult{Name: %q, Status: %s.StatusOK})\n", e.servoAlias, n.Key.String(), e.servoAlias)
		b.WriteString("\t}\n")
	}
	// Ready (and only Ready) reports the emitted servers: readiness is
	// "the listener is bound", which is exactly what each atomic tracks.
	// Health has nothing to say about them — a bound socket is not a
	// health claim.
	if methodName == "Ready" && e.http != nil {
		e.imports.Add("errors", "errors")
		for _, g := range e.http.Groups {
			name := g.stopName()
			fmt.Fprintf(&b, "\tif a.%s != nil && a.%s.ready.Load() {\n", g.Field, g.Field)
			fmt.Fprintf(&b, "\t\tnodes = append(nodes, %s.NodeResult{Name: %q, Status: %s.StatusOK})\n", e.servoAlias, name, e.servoAlias)
			b.WriteString("\t} else {\n")
			fmt.Fprintf(&b, "\t\tnodes = append(nodes, %s.NodeResult{Name: %q, Status: %s.StatusFailed, Err: errors.New(%q)})\n", e.servoAlias, name, e.servoAlias, name+": not accepting connections yet")
			b.WriteString("\t}\n")
		}
	}
	fmt.Fprintf(&b, "\treturn %s.Report{Nodes: nodes}\n", e.servoAlias)
	b.WriteString("}\n\n")
	return b.String()
}
