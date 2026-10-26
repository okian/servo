package servovet

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/route"
)

// checkDirectives is the editor-time half of the //servo: prefix's
// reservation: the generator refuses a malformed directive too, but a
// comment directive's defining hazard is that a typo'd one is just a
// comment — `//servo:confg` compiles, generates, and silently loads
// nothing — so every line under the prefix is judged here, where the
// failure lands as a squiggle instead of a missing setting in production.
//
// The judging is the generator's own: graph.FileDirectives tokenizes and
// attaches every line, a line no reader claims gets the message `servo
// generate` prints for it, a claimed route line is checked against the
// route grammar, and a claimed //servo:config against its options and its
// type. Handler shapes, field tags and cross-route conflicts are left to
// the generator — they need the resolved module, and already fail `servo
// generate` with a position.
//
// One check exists only here: a near miss, a directive written with a
// space after the //. Go reads that as an ordinary comment, and so does
// servo, so `generate` cannot report it without claiming prose — but in
// the editor, a comment that would be a well-formed directive if the space
// went away is worth a squiggle. Generated files are skipped for it, since
// their headers quote directives in prose.
func checkDirectives(pass *analysis.Pass) {
	for _, file := range pass.Files {
		for _, d := range graph.FileDirectives(file) {
			if problem := d.Unclaimed(); problem != "" {
				pass.Reportf(d.Comment.Pos(), "%s", problem)
				continue
			}
			if d.IsRoute() {
				if problem, ok := route.CheckDirective(d.Name, d.Args); !ok {
					pass.Reportf(d.Comment.Pos(), "%s", problem)
				}
				continue
			}
			if _, _, err := graph.ParseConfigDirectiveOptions(d.Args); err != nil {
				pass.Reportf(d.Comment.Pos(), "servo: %v", err)
				continue
			}
			if tn, ok := pass.TypesInfo.Defs[d.Type.Name].(*types.TypeName); ok {
				if _, isStruct := tn.Type().Underlying().(*types.Struct); !isStruct {
					pass.Reportf(d.Comment.Pos(), "servo: //servo:%s is on %s, which is not a struct — the directive generates a loader that fills struct fields from tags", graph.ConfigDirective, d.Type.Name.Name)
				}
			}
		}
		if ast.IsGenerated(file) {
			continue
		}
		for _, group := range file.Comments {
			for _, c := range group.List {
				if fixed, ok := nearMissDirective(c.Text); ok {
					pass.Reportf(c.Pos(), "servo: this comment reads as %s, but the space after // makes it an ordinary comment that `servo generate` never sees — write %s", fixed, fixed)
				}
			}
		}
	}
}

// nearMissDirective reports the directive a comment is one space away from
// being: whitespace between // and servo:, with the rest a well-formed
// route or config directive. Requiring the rest to parse is what keeps
// prose that merely mentions servo: out of it.
func nearMissDirective(text string) (string, bool) {
	body, ok := strings.CutPrefix(text, "//")
	if !ok {
		return "", false
	}
	trimmed := strings.TrimLeft(body, " \t")
	if len(trimmed) == len(body) {
		return "", false // no whitespace: a directive or not servo's at all
	}
	name, args, ok := graph.ParseDirective("//" + trimmed)
	if !ok {
		return "", false
	}
	switch {
	case graph.IsRouteVerb(name):
		if _, ok := route.CheckDirective(name, args); !ok {
			return "", false
		}
	case name == graph.ConfigDirective:
		if _, _, err := graph.ParseConfigDirectiveOptions(args); err != nil {
			return "", false
		}
	default:
		return "", false
	}
	return "//servo:" + strings.Join(append([]string{name}, args...), " "), true
}
