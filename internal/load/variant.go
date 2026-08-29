package load

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// GeneratedConstraint returns the //go:build expression the file generated
// from a spec must carry: the spec file's own constraint with the BuildTag
// term negated — the mirror image that has always been servo's contract —
// conjoined with every tag the load was given.
//
// Mirroring rather than emitting a fixed `!servoinject` is what lets one
// injector have several generated files that coexist. A spec gated
//
//	//go:build servoinject && prod
//
// describes a graph that only makes sense when prod is set, so its output
// gets `!servoinject && prod`; the sibling spec gated `servoinject &&
// !prod` gets `!servoinject && !prod`. The two are mutually exclusive
// because the *author* made them so, in Go's own constraint language,
// rather than because servo remembered something. servo never invents a
// negation, which is why it never has to know the full variant set.
//
// tags are appended as conjuncts because they are what the graph was
// actually resolved under: a provider only visible with -tags=prod must
// not be referenced by a file that compiles without it.
func GeneratedConstraint(file *ast.File, tags []string) (string, error) {
	expr, ok := specConstraint(file)
	if !ok {
		// Unreachable through FindSpecs, which runs checkBuildTag first;
		// reachable if a caller ever builds a Spec by hand.
		return "", fmt.Errorf("spec file has no build constraint requiring %s", BuildTag)
	}
	expr = negateTag(expr, BuildTag)
	// The mirror image is only a mirror if the spec's constraint genuinely
	// required the tag in every satisfying assignment. checkBuildTag's
	// requiresTag decides that by forcing every *other* tag true, which is
	// unsound for a negated disjunct: `servoinject || !prod` passes it, and
	// mirroring yields `!servoinject || !prod`, which is TRUE with
	// servoinject set. Servo would then load its own generated output as
	// part of the next spec-file scan. Before this change the header was
	// the literal !servoinject, so the guarantee was structural; now it has
	// to be checked.
	if satisfiableWith(expr, BuildTag, true) {
		return "", fmt.Errorf("spec file's build constraint does not require %[1]s in every case it allows, so the generated file's mirror image would still compile with %[1]s set and servo would read its own output — gate the spec with %[1]s as a conjunct, as `//go:build %[1]s && ...`", BuildTag)
	}
	for _, tag := range tags {
		// A spec gated `servoinject && prod` generated under -tags=prod
		// already requires prod; appending it again would emit
		// `!servoinject && prod && prod`, which is correct but not
		// something anyone should have to read in a committed file.
		if implies(expr, tag) {
			continue
		}
		expr = &constraint.AndExpr{X: expr, Y: &constraint.TagExpr{Tag: tag}}
	}
	return expr.String(), nil
}

// implies reports whether expr is already false in every configuration
// where tag is unset — i.e. whether adding `&& tag` would change nothing.
//
// Decided exactly rather than syntactically, by enumerating the tags expr
// actually mentions: `!servoinject && (prod || dev)` mentions prod without
// requiring it, and must still gain the conjunct. Expressions in a spec
// file's constraint are small; the ceiling only exists so a pathological
// one degrades to emitting a redundant conjunct instead of hanging.
func implies(expr constraint.Expr, tag string) bool {
	names := tagsIn(expr)
	if !slices.Contains(names, tag) {
		return false
	}
	if len(names) > 16 {
		return false
	}
	for mask := 0; mask < 1<<len(names); mask++ {
		vals := make(map[string]bool, len(names))
		for i, name := range names {
			vals[name] = mask&(1<<i) != 0
		}
		if vals[tag] {
			continue
		}
		// Tags outside expr never come up; default them true so the
		// lookup is total.
		satisfied := expr.Eval(func(t string) bool {
			v, ok := vals[t]
			return !ok || v
		})
		if satisfied {
			return false
		}
	}
	return true
}

// satisfiableWith reports whether expr is true in at least one build
// configuration that pins tag to value, by enumerating every other tag expr
// mentions. Constraint expressions name a handful of tags, so the
// enumeration is tiny; the ceiling only exists so a pathological one
// answers "can't tell" instead of hanging.
func satisfiableWith(expr constraint.Expr, tag string, value bool) bool {
	names := tagsIn(expr)
	if len(names) > enumerationCeiling {
		return false
	}
	for mask := range 1 << len(names) {
		vals := make(map[string]bool, len(names))
		for i, name := range names {
			vals[name] = mask&(1<<i) != 0
		}
		vals[tag] = value
		if expr.Eval(func(t string) bool { return vals[t] }) {
			return true
		}
	}
	return false
}

// ConstraintsOverlap reports whether one build configuration can satisfy
// both expressions at once — which for two generated files means both
// compile together and the package gets duplicate App/New declarations.
//
// Reports false when it cannot tell (an expression naming more tags than
// the enumeration ceiling), because the caller turns true into a refusal
// to generate, and refusing on a guess is worse than the collision.
func ConstraintsOverlap(a, b constraint.Expr) bool {
	names := tagsIn(&constraint.AndExpr{X: a, Y: b})
	if len(names) > enumerationCeiling {
		return false
	}
	for mask := range 1 << len(names) {
		vals := make(map[string]bool, len(names))
		for i, name := range names {
			vals[name] = mask&(1<<i) != 0
		}
		lookup := func(t string) bool { return vals[t] }
		if a.Eval(lookup) && b.Eval(lookup) {
			return true
		}
	}
	return false
}

// ParseConstraint reads a //go:build (or legacy // +build) line as an
// expression — for reading back the constraint of a generated file already
// on disk.
func ParseConstraint(line string) (constraint.Expr, error) {
	return constraint.Parse(line)
}

// enumerationCeiling bounds every satisfiability walk in this file. A spec
// constraint naming more than this many distinct tags is not something
// servo can reason about cheaply, and every caller degrades to a safe
// answer rather than to a wrong one.
const enumerationCeiling = 16

// tagsIn is every distinct tag named anywhere in expr, sorted so the
// enumeration above is deterministic.
func tagsIn(expr constraint.Expr) []string {
	seen := map[string]bool{}
	var walk func(constraint.Expr)
	walk = func(e constraint.Expr) {
		switch e := e.(type) {
		case *constraint.TagExpr:
			seen[e.Tag] = true
		case *constraint.NotExpr:
			walk(e.X)
		case *constraint.AndExpr:
			walk(e.X)
			walk(e.Y)
		case *constraint.OrExpr:
			walk(e.X)
			walk(e.Y)
		}
	}
	walk(expr)
	return slices.Sorted(maps.Keys(seen))
}

// FileConstraint returns the file's build constraint, resolved the way
// go/build.shouldBuild resolves it, or false when the file has none.
//
// Three rules, all of which servo previously got wrong by taking the first
// constraint comment it liked the look of:
//
//   - Only the header counts. A `// +build` line below the package clause
//     is an ordinary comment to the go command, so treating it as a
//     constraint would let servo call a spec file "correctly gated" while
//     it compiles into the real binary.
//   - A `//go:build` line, when present, is the whole constraint; the
//     legacy lines are ignored entirely.
//   - Otherwise every `// +build` line is ANDed. Taking only the first
//     would silently drop the rest — a spec carrying `// +build
//     servoinject` then `// +build !prod` would generate a file gated
//     `!servoinject`, losing the `!prod` that made it a variant.
func FileConstraint(file *ast.File) (constraint.Expr, bool) {
	var plusBuild []constraint.Expr
	for _, group := range file.Comments {
		// Constraints live above the package clause; anything at or after
		// it is a comment, whatever it looks like.
		if group.End() >= file.Package {
			break
		}
		for _, c := range group.List {
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				continue
			}
			if constraint.IsGoBuild(c.Text) {
				return expr, true
			}
			if constraint.IsPlusBuild(c.Text) {
				plusBuild = append(plusBuild, expr)
			}
		}
	}
	if len(plusBuild) == 0 {
		return nil, false
	}
	combined := plusBuild[0]
	for _, expr := range plusBuild[1:] {
		combined = &constraint.AndExpr{X: combined, Y: expr}
	}
	return combined, true
}

// SpecConstraintsIn returns the build constraint of every spec file in
// pkg — the ones this configuration can see and, crucially, the ones it
// excluded.
//
// Answering "does any spec own this generated file?" needs the excluded
// ones: under -tags=prod the default spec is invisible, so a check built
// only from visible specs would call servo_gen.go an orphan on every
// tagged run.
func SpecConstraintsIn(pkg *packages.Package) []constraint.Expr {
	var out []constraint.Expr
	seen := map[string]bool{}
	add := func(path string) {
		if seen[path] || !strings.HasSuffix(path, ".go") {
			return
		}
		seen[path] = true
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			return
		}
		if expr, ok := specConstraint(f); ok {
			out = append(out, expr)
		}
	}
	for _, path := range pkg.GoFiles {
		add(path)
	}
	for _, path := range pkg.IgnoredFiles {
		add(path)
	}
	return out
}

// ConstraintSatisfiedBy reports whether expr holds in the configuration
// where exactly the named tags are set.
func ConstraintSatisfiedBy(expr constraint.Expr, tags []string) bool {
	set := make(map[string]bool, len(tags))
	for _, t := range tags {
		set[t] = true
	}
	return expr.Eval(func(t string) bool { return set[t] })
}

// specConstraint returns the file's constraint when it genuinely requires
// BuildTag — the condition checkBuildTag enforces.
func specConstraint(file *ast.File) (constraint.Expr, bool) {
	expr, ok := FileConstraint(file)
	if !ok || !requiresTag(expr, BuildTag) {
		return nil, false
	}
	return expr, true
}

// negateTag flips every occurrence of tag in expr, leaving every other
// term untouched. `servoinject && !prod` becomes `!servoinject && !prod`.
//
// A `!tag` term flips back to a bare `tag` rather than growing a second
// `!`: `!!x` is not accepted by go/build/constraint's parser, so emitting
// it would produce a generated file the go command refuses to read.
func negateTag(expr constraint.Expr, tag string) constraint.Expr {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		if e.Tag == tag {
			return &constraint.NotExpr{X: e}
		}
		return e
	case *constraint.NotExpr:
		if inner, ok := e.X.(*constraint.TagExpr); ok && inner.Tag == tag {
			return inner
		}
		return &constraint.NotExpr{X: negateTag(e.X, tag)}
	case *constraint.AndExpr:
		return &constraint.AndExpr{X: negateTag(e.X, tag), Y: negateTag(e.Y, tag)}
	case *constraint.OrExpr:
		return &constraint.OrExpr{X: negateTag(e.X, tag), Y: negateTag(e.Y, tag)}
	}
	return expr
}
