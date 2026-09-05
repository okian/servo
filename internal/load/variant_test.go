package load

import (
	"go/build/constraint"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestGeneratedConstraint(t *testing.T) {
	cases := []struct {
		name string
		src  string
		tags []string
		want string
	}{
		{
			name: "no tags mirrors the spec's tag, exactly as servo always has",
			src:  "//go:build servoinject\n\npackage spec\n",
			want: "!servoinject",
		},
		{
			// The whole point: the graph was resolved with prod set, so
			// the file that references its providers must only compile
			// with prod set.
			name: "tag appended",
			src:  "//go:build servoinject\n\npackage spec\n",
			tags: []string{"prod"},
			want: "!servoinject && prod",
		},
		{
			// The negation that makes variants mutually exclusive comes
			// from the author's own spec file, never from servo.
			name: "spec's own negation is carried through",
			src:  "//go:build servoinject && !prod\n\npackage spec\n",
			want: "!servoinject && !prod",
		},
		{
			name: "spec that already requires the tag does not repeat it",
			src:  "//go:build servoinject && prod\n\npackage spec\n",
			tags: []string{"prod"},
			want: "!servoinject && prod",
		},
		{
			// Mentioned but not required, so the conjunct genuinely
			// narrows the constraint and must be added.
			name: "tag mentioned in a disjunction is still appended",
			src:  "//go:build servoinject && (prod || dev)\n\npackage spec\n",
			tags: []string{"prod"},
			want: "!servoinject && (prod || dev) && prod",
		},
		{
			name: "unrelated conditions preserved",
			src:  "//go:build servoinject && !prod\n\npackage spec\n",
			tags: []string{"dev"},
			want: "!servoinject && !prod && dev",
		},
		{
			name: "several tags, in canonical order",
			src:  "//go:build servoinject\n\npackage spec\n",
			tags: []string{"integration", "prod"},
			want: "!servoinject && integration && prod",
		},
		{
			// Contrived — no one writes this — but it is the one shape
			// that reaches the flip-back branch, and the branch has to
			// exist: go/build/constraint's parser rejects !!x, so naively
			// negating every occurrence would emit a constraint the go
			// command cannot read, silently excluding the generated file
			// from every build.
			name: "negated build tag flips back to bare rather than growing a second !",
			src:  "//go:build servoinject && (prod || !servoinject)\n\npackage spec\n",
			want: "!servoinject && (prod || servoinject)",
		},
		{
			name: "old-style +build constraint",
			src:  "// +build servoinject\n\npackage spec\n",
			tags: []string{"prod"},
			want: "!servoinject && prod",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := GeneratedConstraint(parseWithComments(t, c.src), c.tags)
			if err != nil {
				t.Fatalf("GeneratedConstraint: %v", err)
			}
			if got != c.want {
				t.Errorf("GeneratedConstraint = %q, want %q", got, c.want)
			}
		})
	}
}

// TestGeneratedConstraintRequiresAConstraint covers the hand-built-Spec
// path: FindSpecs runs checkBuildTag first, so this is unreachable through
// the CLI, but the function must not emit a constraint it cannot justify.
func TestGeneratedConstraintRequiresAConstraint(t *testing.T) {
	_, err := GeneratedConstraint(parseWithComments(t, "package spec\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "no build constraint") {
		t.Fatalf("got err=%v, want a missing-constraint error", err)
	}
}

// TestGeneratedConstraintRoundTrips pins the three properties that matter
// about whatever servo emits, across every spec shape and tag set that can
// actually occur: the go command must be able to parse it back (an
// unparseable constraint would exclude the generated file from every build,
// silently); it must be false whenever the build tag is set, which is what
// stops `servo generate` from ever reading its own output; and it must be
// true somewhere, or servo has written a file that compiles nowhere.
func TestGeneratedConstraintRoundTrips(t *testing.T) {
	srcs := []string{
		"//go:build servoinject\n\npackage spec\n",
		"//go:build servoinject && !prod\n\npackage spec\n",
		"//go:build servoinject && prod\n\npackage spec\n",
		"//go:build servoinject && (prod || dev)\n\npackage spec\n",
		"//go:build servoinject && (prod || !servoinject)\n\npackage spec\n",
		"// +build servoinject\n\npackage spec\n",
	}
	for _, src := range srcs {
		for _, tags := range [][]string{nil, {"prod"}, {"integration", "prod"}} {
			file := parseWithComments(t, src)
			specExpr, ok := specConstraint(file)
			if !ok {
				t.Fatalf("%q: no spec constraint", src)
			}
			// Only pairs a real run can produce. A spec gated `!prod` is
			// invisible under -tags=prod, so FindSpecs would never hand it
			// to GeneratedConstraint, and asking for its constraint would
			// be asking about a contradiction that cannot arise.
			if !specExpr.Eval(activeTags(append([]string{BuildTag}, tags...))) {
				continue
			}

			got, err := GeneratedConstraint(file, tags)
			if err != nil {
				t.Fatalf("GeneratedConstraint(%q, %v): %v", src, tags, err)
			}
			emitted, perr := constraint.Parse("//go:build " + got)
			if perr != nil {
				t.Fatalf("spec %q + %v emitted %q, which go/build/constraint cannot parse: %v", src, tags, got, perr)
			}
			if satisfiable(emitted, true) {
				t.Errorf("spec %q + %v emitted %q, which can be true with %s set — servo would read its own output", src, tags, got, BuildTag)
			}
			if !satisfiable(emitted, false) {
				t.Errorf("spec %q + %v emitted %q, which is true in no configuration at all — the generated file would compile nowhere", src, tags, got)
			}
		}
	}
}

// activeTags models a build configuration: the named tags are set and
// every other tag is not.
func activeTags(tags []string) func(string) bool {
	set := make(map[string]bool, len(tags))
	for _, t := range tags {
		set[t] = true
	}
	return func(t string) bool { return set[t] }
}

// satisfiable reports whether expr is true in at least one configuration
// with BuildTag pinned to the given value, by enumerating every tag expr
// mentions.
func satisfiable(expr constraint.Expr, buildTag bool) bool {
	names := tagsIn(expr)
	for mask := range 1 << len(names) {
		vals := map[string]bool{}
		for i, name := range names {
			vals[name] = mask&(1<<i) != 0
		}
		vals[BuildTag] = buildTag
		if expr.Eval(func(t string) bool { return vals[t] }) {
			return true
		}
	}
	return false
}

func TestTagsIn(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{"//go:build servoinject\n\npackage spec\n", []string{"servoinject"}},
		{"//go:build servoinject && !prod\n\npackage spec\n", []string{"prod", "servoinject"}},
		{"//go:build servoinject && (a || b)\n\npackage spec\n", []string{"a", "b", "servoinject"}},
		{"//go:build servoinject && a && a\n\npackage spec\n", []string{"a", "servoinject"}},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			expr, ok := specConstraint(parseWithComments(t, c.src))
			if !ok {
				t.Fatal("no constraint found")
			}
			got := tagsIn(expr)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("tagsIn = %v, want %v", got, c.want)
			}
		})
	}
}

func TestImplies(t *testing.T) {
	cases := []struct {
		src  string
		tag  string
		want bool
	}{
		{"//go:build servoinject && prod\n\npackage spec\n", "prod", true},
		{"//go:build servoinject\n\npackage spec\n", "prod", false},
		{"//go:build servoinject && (prod || dev)\n\npackage spec\n", "prod", false},
		{"//go:build servoinject && !prod\n\npackage spec\n", "prod", false},
		{"//go:build servoinject && prod && dev\n\npackage spec\n", "dev", true},
	}
	for _, c := range cases {
		t.Run(c.src+" "+c.tag, func(t *testing.T) {
			expr, ok := specConstraint(parseWithComments(t, c.src))
			if !ok {
				t.Fatal("no constraint found")
			}
			if got := implies(negateTag(expr, BuildTag), c.tag); got != c.want {
				t.Errorf("implies(%q, %q) = %v, want %v", c.src, c.tag, got, c.want)
			}
		})
	}
}

func TestFileConstraint(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // "" means no constraint
	}{
		{"go:build", "//go:build servoinject\n\npackage spec\n", "servoinject"},
		{"none", "package spec\n", ""},
		{"plain doc comment", "// Package spec wires the app.\npackage spec\n", ""},
		{"single +build", "// +build servoinject\n\npackage spec\n", "servoinject"},
		// go/build ANDs every +build line. Taking only the first silently
		// dropped the rest, so a spec meaning `servoinject && !prod`
		// generated a file gated `!servoinject` — losing exactly the term
		// that made it a variant.
		{
			"several +build lines are ANDed",
			"// +build servoinject\n// +build !prod\n\npackage spec\n",
			"servoinject && !prod",
		},
		{
			"three +build lines",
			"// +build servoinject\n// +build !prod\n// +build !dev\n\npackage spec\n",
			"servoinject && !prod && !dev",
		},
		// When both syntaxes appear, go/build uses //go:build alone.
		{
			"go:build wins over +build",
			"//go:build servoinject && !prod\n// +build servoinject\n\npackage spec\n",
			"servoinject && !prod",
		},
		// Below the package clause it is an ordinary comment to the go
		// command, so servo must not call the file gated on its account —
		// it would compile straight into the real binary.
		{
			"constraint below the package clause is not a constraint",
			"package spec\n\n// +build servoinject\n",
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expr, ok := FileConstraint(parseWithComments(t, c.src))
			if c.want == "" {
				if ok {
					t.Fatalf("FileConstraint(%q) = %q, want none", c.src, expr.String())
				}
				return
			}
			if !ok {
				t.Fatalf("FileConstraint(%q) found no constraint, want %q", c.src, c.want)
			}
			if got := expr.String(); got != c.want {
				t.Errorf("FileConstraint(%q) = %q, want %q", c.src, got, c.want)
			}
		})
	}
}

// TestGeneratedConstraintRefusesToMirrorANonRequiringConstraint covers the
// hole in checkBuildTag's heuristic. requiresTag decides "can only be true
// with the tag" by forcing every other tag true, which a negated disjunct
// defeats: `servoinject || !prod` passes, and its mirror image
// `!servoinject || !prod` is TRUE with servoinject set — so the next spec
// scan would load servo's own generated output as a source file. Emitting
// a constraint is now conditional on that not being possible.
func TestGeneratedConstraintRefusesToMirrorANonRequiringConstraint(t *testing.T) {
	_, err := GeneratedConstraint(parseWithComments(t, "//go:build servoinject || !prod\n\npackage spec\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "servo would read its own output") {
		t.Fatalf("got err=%v, want a refusal naming the self-read hazard", err)
	}
}

func TestConstraintsOverlap(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		// The footgun this exists to catch: what `servo init` scaffolds,
		// generated a second time with --tags=prod.
		{"unconstrained default vs a tagged variant", "!servoinject", "!servoinject && prod", true},
		// What an author gets when they gate the specs properly.
		{"properly excluded pair", "!servoinject && !prod", "!servoinject && prod", false},
		{"three-way exclusive", "!servoinject && !prod && !dev", "!servoinject && prod", false},
		{"identical", "!servoinject && prod", "!servoinject && prod", true},
		{"disjunction that still overlaps", "!servoinject && (prod || dev)", "!servoinject && dev", true},
		{"unrelated tags overlap, since both can be set", "!servoinject && a", "!servoinject && b", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := ParseConstraint("//go:build " + c.a)
			if err != nil {
				t.Fatal(err)
			}
			b, err := ParseConstraint("//go:build " + c.b)
			if err != nil {
				t.Fatal(err)
			}
			if got := ConstraintsOverlap(a, b); got != c.want {
				t.Errorf("ConstraintsOverlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestSpecConstraintsInIncludesTheSpecsThisConfigurationExcluded is the
// property the function exists for. Asking "does any spec own this
// generated file?" from the visible specs alone gets the answer wrong
// exactly when it matters: under -tags=prod the default spec is excluded
// from the build, so servo_gen.go — which that spec owns — would be
// reported as an orphan on every tagged run.
//
// The package is assembled by hand rather than loaded, because the point
// is which of GoFiles/IgnoredFiles the scan reads, and a real load can
// only ever demonstrate one configuration at a time.
func TestSpecConstraintsInIncludesTheSpecsThisConfigurationExcluded(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, src string) string {
		mustWriteFile(t, dir, rel, src)
		return filepath.Join(dir, rel)
	}
	visibleSpec := write("spec_default.go", "//go:build servoinject && !prod\n\npackage main\n")
	excludedSpec := write("spec_prod.go", "//go:build servoinject && prod\n\npackage main\n")
	// Servo's own output. It mentions the build tag, but negated, so it
	// requires the tag to be *absent* — reading it back as a spec would
	// make every generated file look like it owned itself.
	generated := write("servo_gen.go", "//go:build !servoinject\n\npackage main\n")
	plain := write("main.go", "package main\n\nfunc main() {}\n")
	// A non-Go source excluded by a constraint, and a path that no longer
	// exists: neither is evidence of a spec, and neither may abort the
	// scan and lose the excluded spec that follows it.
	asm := write("stub_prod.s", "// +build prod\n")
	deleted := filepath.Join(dir, "gone.go")

	pkg := &packages.Package{
		GoFiles: []string{visibleSpec, generated, plain},
		// excludedSpec twice: one spec file must contribute one
		// constraint however many times its path reaches the scan.
		IgnoredFiles: []string{excludedSpec, asm, deleted, excludedSpec},
	}

	var got []string
	for _, expr := range SpecConstraintsIn(pkg) {
		got = append(got, expr.String())
	}
	want := []string{"servoinject && !prod", "servoinject && prod"}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("SpecConstraintsIn = %v, want %v", got, want)
	}
}

// TestConstraintSatisfiedBy covers the question its caller actually asks:
// given the tag set a generated file's name encodes, is there a spec whose
// own constraint holds there — that is, did some spec in this package
// really produce that file. The build tag is part of the configuration a
// spec is read in, so it is passed explicitly rather than assumed.
func TestConstraintSatisfiedBy(t *testing.T) {
	cases := []struct {
		name string
		expr string
		tags []string
		want bool
	}{
		{"the prod spec owns the prod variant", "servoinject && prod", []string{"servoinject", "prod"}, true},
		{"the prod spec does not own the default file", "servoinject && prod", []string{"servoinject"}, false},
		{"the default spec owns the default file", "servoinject && !prod", []string{"servoinject"}, true},
		{"the default spec does not own the prod variant", "servoinject && !prod", []string{"servoinject", "prod"}, false},
		// An ungated default spec claims every variant, which is exactly
		// the overlap `servo doctor` reports: it is what `servo init`
		// scaffolds, generated a second time under a tag.
		{"an ungated spec owns everything", "servoinject", []string{"servoinject", "prod"}, true},
		// Nothing is set at all, not even the build tag: no spec is
		// readable in that configuration, so none owns anything.
		{"no tags at all satisfies no spec", "servoinject && prod", nil, false},
		{"a disjunction is satisfied by either side", "servoinject && (prod || dev)", []string{"servoinject", "dev"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expr, err := ParseConstraint("//go:build " + c.expr)
			if err != nil {
				t.Fatal(err)
			}
			if got := ConstraintSatisfiedBy(expr, c.tags); got != c.want {
				t.Errorf("ConstraintSatisfiedBy(%q, %v) = %v, want %v", c.expr, c.tags, got, c.want)
			}
		})
	}
}
