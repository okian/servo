package servovet

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// checkCapabilityNearMisses flags a method that has a capability's name but
// not its signature, on a type a constructor in this package returns.
//
// Capabilities are detected by types.Implements and nothing else, so
// `Run() error`, a Stop with no error result, or an Init without a context
// is not Initializer-ish to servo — it is simply not called, and the
// component reports "capabilities: none" while its author believes its
// loop runs. `servo generate` notes the same thing for the nodes of a
// resolved graph; this pass reaches it earlier, and for types no injector
// has reached yet.
//
// Only the result types of constructor-shaped functions in this package
// are examined — the shape that could be a graph node — and only methods
// declared in this package, so an embedded dependency's own Run is not this
// package's to fix. Generated files are skipped on both sides: the
// generated App has Health and Ready methods of its own, with a different
// contract, and New returns it.
func checkCapabilityNearMisses(pass *analysis.Pass) {
	generated := map[*token.File]bool{}
	for _, f := range pass.Files {
		if ast.IsGenerated(f) {
			generated[pass.Fset.File(f.Pos())] = true
		}
	}
	inGenerated := func(pos token.Pos) bool { return generated[pass.Fset.File(pos)] }

	pkg := &packages.Package{Name: pass.Pkg.Name(), PkgPath: pass.Pkg.Path(), Types: pass.Pkg, Fset: pass.Fset}
	accepted, _ := graph.ScanCandidates([]*packages.Package{pkg}, pass.Pkg.Path())
	// Each method once, however many constructors return its type.
	reported := map[*types.Func]bool{}
	for _, p := range accepted {
		if inGenerated(p.Func.Pos()) {
			continue
		}
		for _, miss := range graph.CapabilityNearMisses(p.ResultType) {
			m := miss.Method
			if m.Pkg() != pass.Pkg || inGenerated(m.Pos()) || reported[m] {
				continue
			}
			reported[m] = true
			pass.Reportf(m.Pos(), "servo: %s %s, so servo will never call it — it has the name of servo's %s capability, which is detected by signature alone", m.Name(), miss.Problem, miss.Capability)
		}
	}
}
