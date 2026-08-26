package emit

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v2/internal/resolve"
)

// newFunc emits the constructor: sequential DFS-post-order construction
// with statically unrolled rollback, then the Init phase.
func (e *emitter) newFunc() string {
	var b strings.Builder
	fmt.Fprintf(&b, "func %s(ctx context.Context) (*%s, error) {\n", e.constructorName(), e.appType())
	fmt.Fprintf(&b, "\ta := &%s{}\n\n", e.appType())

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
		args[i] = e.varName[dep.Key]
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
		fmt.Fprintf(b, "\t\t_ = a.stop%s(ctx)\n", capitalize(e.varName[n.Key]))
	}
}

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
	b.WriteString("\t\t\treport := a.Shutdown(ctx)\n")
	b.WriteString("\t\t\treturn nil, errors.Join(err, report)\n")
	b.WriteString("\t\t}\n\t}\n")
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
	b.WriteString("\t\t\treport := a.Shutdown(ctx)\n")
	b.WriteString("\t\t\treturn nil, errors.Join(err, report)\n")
	b.WriteString("\t\t}\n\t}\n")
}
