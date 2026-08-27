package emit

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v3/internal/resolve"
)

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

	fmt.Fprintf(b, "func (a *%s) stop%s(ctx context.Context) %s.NodeResult {\n", e.appType(), capitalize(name), e.servoAlias)
	fmt.Fprintf(b, "\ta.%sStopOnce.Do(func() {\n", name)
	fmt.Fprintf(b, "\t\tvar results []%s.NodeResult\n", e.servoAlias)

	if hasCapability(n, "Drainer") {
		fmt.Fprintf(b, "\t\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, a.%s.Drain))\n", e.servoAlias, e.servoAlias, label, name)
	}
	if hasCapability(n, "Flusher") {
		fmt.Fprintf(b, "\t\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, a.%s.Flush))\n", e.servoAlias, e.servoAlias, label, name)
	}
	if hasCapability(n, "Finalizer") {
		fmt.Fprintf(b, "\t\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, a.%s.Stop))\n", e.servoAlias, e.servoAlias, label, name)
	}
	if n.Provider.HasCleanup {
		fmt.Fprintf(b, "\t\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, func(context.Context) error { a.%sCleanup(); return nil }))\n", e.servoAlias, e.servoAlias, label, name)
	}

	fmt.Fprintf(b, "\t\ta.%sStopResult = %s.MergeNodeResults(%q, results...)\n", name, e.servoAlias, label)
	b.WriteString("\t})\n")
	fmt.Fprintf(b, "\treturn a.%sStopResult\n", name)
	b.WriteString("}\n\n")
}

// runFunc launches every Runner into an errgroup; one Runner's error
// cancels the shared context so the others stop too, and Run does not
// return until all have. Special-cased for zero/one runners since most
// graphs have exactly one long-running server and an errgroup of one adds
// nothing but noise.
func (e *emitter) runFunc() string {
	runners := []*resolve.Node{}
	for _, n := range e.resolved.Order {
		if hasCapability(n, "Runner") {
			runners = append(runners, n)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "func (a *%s) Run(ctx context.Context) error {\n", e.appType())
	switch len(runners) {
	case 0:
		b.WriteString("\treturn nil\n")
	case 1:
		fmt.Fprintf(&b, "\treturn a.%s.Run(ctx)\n", e.varName[runners[0].Key])
	default:
		e.imports.Add("golang.org/x/sync/errgroup", "errgroup")
		b.WriteString("\tg, gctx := errgroup.WithContext(ctx)\n")
		for _, n := range runners {
			fmt.Fprintf(&b, "\tg.Go(func() error { return a.%s.Run(gctx) })\n", e.varName[n.Key])
		}
		b.WriteString("\treturn g.Wait()\n")
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
	b.WriteString("\tgo func() {\n\t\tselect {\n\t\tcase <-forceExit:\n\t\t\tos.Exit(1)\n\t\tcase <-watcherDone:\n\t\t}\n\t}()\n\n")

	fmt.Fprintf(&b, "\tvar nodes []%s.NodeResult\n", e.servoAlias)
	for i := len(e.resolved.Order) - 1; i >= 0; i-- {
		n := e.resolved.Order[i]
		if !e.needsStopMethod(n) {
			continue
		}
		fmt.Fprintf(&b, "\tnodes = append(nodes, a.stop%s(ctx))\n", capitalize(e.varName[n.Key]))
	}
	fmt.Fprintf(&b, "\treturn %s.Report{Nodes: nodes}\n", e.servoAlias)
	b.WriteString("}\n\n")
	return b.String()
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
	fmt.Fprintf(&b, "\treturn %s.Report{Nodes: nodes}\n", e.servoAlias)
	b.WriteString("}\n\n")
	return b.String()
}
