package load

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func parseWithComments(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "spec.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}

// TestFileRequiresBuildTagCompoundExpressions exercises requiresTag's
// "force every other tag true" heuristic against real compound
// //go:build expressions, not just the bare `servoinject` case every
// other test uses.
func TestFileRequiresBuildTagCompoundExpressions(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "bare tag",
			src:  "//go:build servoinject\n\npackage spec\n",
			want: true,
		},
		{
			name: "AND with other tags: still requires it",
			src:  "//go:build (linux || darwin) && servoinject\n\npackage spec\n",
			want: true,
		},
		{
			name: "OR with another tag: does NOT require it",
			src:  "//go:build linux || servoinject\n\npackage spec\n",
			want: false,
		},
		{
			name: "negated tag: never requires it",
			src:  "//go:build !servoinject\n\npackage spec\n",
			want: false,
		},
		{
			name: "unrelated constraint only",
			src:  "//go:build linux\n\npackage spec\n",
			want: false,
		},
		{
			name: "no build constraint at all",
			src:  "package spec\n",
			want: false,
		},
		{
			name: "old-style +build, still recognized",
			src:  "// +build servoinject\n\npackage spec\n",
			want: true,
		},
		{
			name: "ordinary doc comment, not a build constraint at all",
			src:  "// Package spec wires the app.\npackage spec\n",
			want: false,
		},
		{
			name: "malformed //go:build expression is skipped, not fatal",
			src:  "//go:build &&\n\npackage spec\n",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := parseWithComments(t, c.src)
			if got := FileRequiresBuildTag(f, "servoinject"); got != c.want {
				t.Errorf("FileRequiresBuildTag(%q) = %v, want %v", c.src, got, c.want)
			}
		})
	}
}

// TestCheckBuildTagRejectsConstraintThatDoesNotTrulyGate confirms the
// end-to-end consequence of the OR case above: a file that merely
// mentions servoinject without actually requiring it is rejected by
// checkBuildTag with the same diagnostic as no constraint at all.
func TestCheckBuildTagRejectsConstraintThatDoesNotTrulyGate(t *testing.T) {
	f := parseWithComments(t, "//go:build linux || servoinject\n\npackage spec\n")
	spec := &Spec{File: f}
	if err := checkBuildTag(spec); err == nil {
		t.Fatal("expected checkBuildTag to reject a constraint satisfiable without servoinject")
	}
}
