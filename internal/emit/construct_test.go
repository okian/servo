package emit

import (
	"fmt"
	"strings"
	"testing"
)

// TestEmitConstructionRollbackIsLinear: the rollback used to be unrolled
// at every failure site, a stop call for each stoppable node built before
// it, so a graph whose constructors can all fail and whose nodes all stop
// emitted n(n-1)/2 stop calls — at five hundred nodes a 136,000-line file
// that took generate ten minutes and 4 GB to compile. Each node now
// registers its own stop once and each failure site is one call, so the
// file grows linearly however the two kinds interleave.
func TestEmitConstructionRollbackIsLinear(t *testing.T) {
	const n = 300
	var src strings.Builder
	src.WriteString("package app\n\nimport \"context\"\n\n")
	params := make([]string, n)
	for i := range n {
		fmt.Fprintf(&src, "type T%[1]d struct{}\n\nfunc NewT%[1]d() (*T%[1]d, error) { return &T%[1]d{}, nil }\n\nfunc (*T%[1]d) Stop(ctx context.Context) error { return nil }\n\n", i)
		params[i] = fmt.Sprintf("t%[1]d *T%[1]d", i)
	}
	fmt.Fprintf(&src, "type Root struct{}\n\nfunc NewRoot(%s) *Root { return &Root{} }\n", strings.Join(params, ", "))

	resolved, spec := buildResolvedFrom(t, src.String(), []string{"Root"}, nil)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	gen := string(out)

	if got := strings.Count(gen, "undo = append(undo, a.stopT"); got != n {
		t.Errorf("%d nodes registered their stop, want all %d, once each", got, n)
	}
	// T0 is built first, so its own failure has nothing before it to stop.
	if got := strings.Count(gen, "a.unwind(context.WithoutCancel(ctx), undo)"); got != n-1 {
		t.Errorf("%d unwind call sites, want %d: one per constructor that can fail with something built before it", got, n-1)
	}
	if got := strings.Count(gen, "_ = a.stopT"); got != 0 {
		t.Errorf("%d stop calls unrolled at failure sites; each node's stop belongs in the undo list alone", got)
	}
	if lines := strings.Count(gen, "\n"); lines > 40*n {
		t.Errorf("%d lines for %d nodes: the file is no longer linear in the graph", lines, n)
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", src.String()})
}

// rollbackSrc interleaves the four shapes a construction failure has to
// tell apart: a stoppable node that cannot fail (Alpha), a fallible node
// with nothing to stop (Bravo), a fallible stoppable node (Charlie), and a
// fallible stoppable node that does fail (Delta, at run time) with one
// more after it that is never reached (Echo). Every constructor and stop
// appends to Log, so a run can assert exactly what happened.
const rollbackSrc = `
package app

import (
	"context"
	"errors"
)

var Log []string

var ErrDelta = errors.New("delta failed")

type Alpha struct{}

func NewAlpha() *Alpha { Log = append(Log, "build Alpha"); return &Alpha{} }
func (*Alpha) Stop(ctx context.Context) error { Log = append(Log, "stop Alpha"); return nil }

type Bravo struct{}

func NewBravo(a *Alpha) (*Bravo, error) { Log = append(Log, "build Bravo"); return &Bravo{}, nil }

type Charlie struct{}

func NewCharlie(b *Bravo) (*Charlie, func(), error) {
	Log = append(Log, "build Charlie")
	return &Charlie{}, func() { Log = append(Log, "cleanup Charlie") }, nil
}
func (*Charlie) Stop(ctx context.Context) error { Log = append(Log, "stop Charlie"); return nil }

type Delta struct{}

func NewDelta(c *Charlie) (*Delta, error) { Log = append(Log, "build Delta"); return nil, ErrDelta }
func (*Delta) Stop(ctx context.Context) error { Log = append(Log, "stop Delta"); return nil }

type Echo struct{}

func NewEcho(d *Delta) (*Echo, error) { Log = append(Log, "build Echo"); return &Echo{}, nil }
func (*Echo) Stop(ctx context.Context) error { Log = append(Log, "stop Echo"); return nil }
`

// TestEmitConstructionRollbackShape pins where the undo list is written:
// a stoppable node registers right after its fields are set, a failure
// site with something before it calls the unwind, and one without — the
// first stoppable node's own constructor — returns plainly.
func TestEmitConstructionRollbackShape(t *testing.T) {
	resolved, spec := buildResolvedFrom(t, rollbackSrc, []string{"Echo"}, nil)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	gen := string(out)
	for _, want := range []string{
		"\tvar undo []func(context.Context) servo.NodeResult\n\n\talpha := NewAlpha()\n\ta.alpha = alpha\n\tundo = append(undo, a.stopAlpha)\n",
		"bravo, err := NewBravo(alpha)\n\tif err != nil {\n\t\ta.unwind(context.WithoutCancel(ctx), undo)\n\t\treturn nil, err\n\t}\n\ta.bravo = bravo\n\n",
		"\ta.charlie = charlie\n\ta.charlieCleanup = charlieCleanup\n\tundo = append(undo, a.stopCharlie)\n",
		"func (a *App) unwind(ctx context.Context, undo []func(context.Context) servo.NodeResult) {\n\tfor i := len(undo) - 1; i >= 0; i-- {\n\t\t_ = undo[i](ctx)\n\t}\n}\n",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, gen)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", rollbackSrc})
}

// TestEmitConstructionRollbackRuns drives the generated New until Delta
// fails: what it built before Delta and has something to stop is stopped,
// newest first, cleanup included; Bravo has nothing to stop; Echo and
// Delta itself were never built; and New returns Delta's own error.
func TestEmitConstructionRollbackRuns(t *testing.T) {
	resolved, spec := buildResolvedFrom(t, rollbackSrc, []string{"Echo"}, nil)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	runGenerated(t, "app", out, map[string]string{
		"app/app.go": rollbackSrc,
		"app/rollback_test.go": `package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRollback(t *testing.T) {
	app, err := New(context.Background())
	if app != nil || !errors.Is(err, ErrDelta) || err.Error() != ErrDelta.Error() {
		t.Fatalf("New = %v, %v; want nil and Delta's own error", app, err)
	}
	got := strings.Join(Log, ", ")
	want := "build Alpha, build Bravo, build Charlie, build Delta, stop Charlie, cleanup Charlie, stop Alpha"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
`,
	})
}

// TestEmitConstructionRollbackOnlyWhenSomethingCanBeUndone: a graph whose
// constructors cannot fail, or fail only before anything stoppable is
// built, has nothing to unwind, and gets neither the list nor the method.
func TestEmitConstructionRollbackOnlyWhenSomethingCanBeUndone(t *testing.T) {
	for name, src := range map[string]string{
		"nothing can fail": `
package app

import "context"

type Alpha struct{}

func NewAlpha() *Alpha { return &Alpha{} }
func (*Alpha) Stop(ctx context.Context) error { return nil }

type Bravo struct{}

func NewBravo(a *Alpha) *Bravo { return &Bravo{} }
func (*Bravo) Stop(ctx context.Context) error { return nil }
`,
		"failures come first": `
package app

import "context"

type Alpha struct{}

func NewAlpha() (*Alpha, error) { return &Alpha{}, nil }
func (*Alpha) Stop(ctx context.Context) error { return nil }

type Bravo struct{}

func NewBravo(a *Alpha) *Bravo { return &Bravo{} }
func (*Bravo) Stop(ctx context.Context) error { return nil }
`,
	} {
		t.Run(name, func(t *testing.T) {
			resolved, spec := buildResolvedFrom(t, src, []string{"Bravo"}, nil)
			out, err := Emit(resolved, spec, false)
			if err != nil {
				t.Fatalf("Emit: %v", err)
			}
			if strings.Contains(string(out), "undo") || strings.Contains(string(out), "unwind") {
				t.Errorf("nothing can be unwound, so nothing should be emitted for it:\n%s", out)
			}
			typecheckGenerated(t, out, fixturePkg{"example.com/app", src})
		})
	}
}

// TestEmitConstructionRollbackStepsAroundUserNames: the list is a local
// in New and the unwind an App method, so nodes spelled like either keep
// their own names and the generated ones move instead.
func TestEmitConstructionRollbackStepsAroundUserNames(t *testing.T) {
	const src = `
package app

import "context"

type Undo struct{}

func NewUndo() *Undo { return &Undo{} }
func (*Undo) Stop(ctx context.Context) error { return nil }

type Unwind struct{}

func NewUnwind(u *Undo) (*Unwind, error) { return &Unwind{}, nil }
`
	resolved, spec := buildResolvedFrom(t, src, []string{"Unwind"}, nil)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	gen := string(out)
	for _, want := range []string{
		"var undo2 []func(context.Context) servo.NodeResult",
		"\tundo := NewUndo()\n\ta.undo = undo\n\tundo2 = append(undo2, a.stopUndo)\n",
		"unwind, err := NewUnwind(undo)\n\tif err != nil {\n\t\ta.unwind2(context.WithoutCancel(ctx), undo2)\n",
		"func (a *App) unwind2(ctx context.Context, undo []func(context.Context) servo.NodeResult) {",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, gen)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", src})
}

// TestEmitConstructionRollbackAliasesAPackageNamedUndo: the list is
// declared before every construction, in the scope the constructors'
// package qualifiers are written in, so a package named undo that a later
// constructor is called through is aliased rather than hidden behind it.
func TestEmitConstructionRollbackAliasesAPackageNamedUndo(t *testing.T) {
	const undoPkg = `
package undo

import "context"

type Journal struct{}

func NewJournal() (*Journal, error) { return &Journal{}, nil }
func (*Journal) Stop(ctx context.Context) error { return nil }
`
	const appSrc = `
package app

import (
	"context"

	"example.com/undo"
)

type Clock struct{}

func NewClock() *Clock { return &Clock{} }
func (*Clock) Stop(ctx context.Context) error { return nil }

type Root struct{}

func NewRoot(c *Clock, j *undo.Journal) *Root { return &Root{} }
`
	resolved, spec := buildResolvedFromPkgs(t, []string{"Root"}, fixturePkg{"example.com/undo", undoPkg}, fixturePkg{"example.com/app", appSrc})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	gen := string(out)
	for _, want := range []string{
		`examplecomundo "example.com/undo"`,
		"var undo []func(context.Context) servo.NodeResult",
		"journal, err := examplecomundo.NewJournal()\n\tif err != nil {\n\t\ta.unwind(context.WithoutCancel(ctx), undo)\n",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, gen)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/undo", undoPkg}, fixturePkg{"example.com/app", appSrc})
}
