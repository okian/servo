package servovet

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"

	"github.com/okian/servo/v3/internal/graph"
)

// checkConfigDirectives is the editor-time half of //servo:config
// validation: the generator refuses a malformed directive too, but a
// comment directive's defining hazard is that a typo'd one is just a
// comment — `//servo:confg` compiles, generates, and silently loads
// nothing — so the whole space of `//servo:` lines is checked here, where
// the failure lands as a squiggle instead of a missing setting in
// production.
//
// Three mistakes are reported: a directive name servo doesn't define, a
// //servo:config whose options don't parse (or whose type isn't a
// struct), and a well-formed directive attached to anything other than a
// type declaration, where the generator would never look. Field tags are
// deliberately left to the generator — they need the full type-checked
// struct, and a wrong tag already fails `servo generate` with a position.
func checkConfigDirectives(pass *analysis.Pass) {
	for _, file := range pass.Files {
		typeDoc := typeDocComments(file)
		for _, group := range file.Comments {
			for _, c := range group.List {
				name, rest, ok := graph.DirectiveLine(c.Text)
				if !ok {
					continue
				}
				if name != graph.ConfigDirective {
					pass.Reportf(c.Pos(), "servo: unrecognized directive //servo:%s — the only comment directive is //servo:%s", name, graph.ConfigDirective)
					continue
				}
				spec, attached := typeDoc[c]
				if !attached {
					pass.Reportf(c.Pos(), "servo: //servo:%s is not attached to a type declaration's doc comment, so `servo generate` will never see it", graph.ConfigDirective)
					continue
				}
				if _, _, err := graph.ParseConfigDirectiveOptions(rest); err != nil {
					pass.Reportf(c.Pos(), "servo: %v", err)
					continue
				}
				if tn, ok := pass.TypesInfo.Defs[spec.Name].(*types.TypeName); ok {
					if _, isStruct := tn.Type().Underlying().(*types.Struct); !isStruct {
						pass.Reportf(c.Pos(), "servo: //servo:%s is on %s, which is not a struct — the directive generates a loader that fills struct fields from tags", graph.ConfigDirective, spec.Name.Name)
					}
				}
			}
		}
	}
}

// typeDocComments maps each comment that belongs to a type declaration's
// doc — the TypeSpec's own, or the GenDecl's for the common single-type
// declaration — back to that TypeSpec, mirroring exactly where the
// generator reads directives from.
func typeDocComments(file *ast.File) map[*ast.Comment]*ast.TypeSpec {
	out := map[*ast.Comment]*ast.TypeSpec{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, s := range gen.Specs {
			ts, ok := s.(*ast.TypeSpec)
			if !ok {
				continue
			}
			doc := ts.Doc
			if doc == nil && len(gen.Specs) == 1 {
				doc = gen.Doc
			}
			if doc == nil {
				continue
			}
			for _, c := range doc.List {
				out[c] = ts
			}
		}
	}
	return out
}
