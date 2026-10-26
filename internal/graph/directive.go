package graph

import (
	"fmt"
	"go/ast"
	"go/token"
	"slices"
	"strings"
)

// directivePrefix is what every servo comment directive starts with, and
// the whole prefix is reserved. The names after it form a closed set —
// config on a type declaration, the route verbs on a function — and a line
// under the prefix that is not one of those, where it belongs, is an error
// rather than a skipped comment: a typo'd directive is otherwise just a
// comment that compiles, generates, and silently does nothing.
const directivePrefix = "//servo:"

// RouteVerbs are the //servo: route directive names, in the order the
// diagnostics list them. They live here, beside the config directive,
// rather than in internal/route because the claim rules below need both
// families and graph cannot import route; route builds its method table
// from this list.
var RouteVerbs = []string{"get", "post", "put", "patch", "delete", "head", "options"}

// RouteVerbList spells RouteVerbs for the diagnostics that name them.
var RouteVerbList = strings.Join(RouteVerbs, ", ")

// IsRouteVerb reports whether name is one of RouteVerbs.
func IsRouteVerb(name string) bool {
	return slices.Contains(RouteVerbs, name)
}

// GroupNameGrammar is the grammar a listener-group name satisfies in both
// places one is written — a servo.Group argument, read by internal/load,
// and a route directive's trailing token, read by internal/route. It is
// defined once so the two cannot drift apart; GroupNamePattern anchors it
// for a whole-string match.
const (
	GroupNameGrammar = `[A-Za-z0-9_-]+`
	GroupNamePattern = `^` + GroupNameGrammar + `$`
)

// ParseDirective tokenizes a comment line under the //servo: prefix: name
// is the first whitespace-separated token after the prefix, args the rest.
// ok is false only for a comment outside the prefix; a bare //servo: is a
// directive with no name, which every reader refuses.
//
// It is the one tokenizer every reader of the prefix uses — the route scan,
// the config scan and servovet — so a tab where a space was expected, or a
// space after the colon, means the same thing to `servo generate` as it
// does in the editor. They used to disagree: generate split on any
// whitespace and servovet on a single space, so `//servo:get<TAB>/orders`
// was a served route that the editor called unrecognized.
func ParseDirective(comment string) (name string, args []string, ok bool) {
	body, ok := strings.CutPrefix(comment, directivePrefix)
	if !ok {
		return "", nil, false
	}
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return "", nil, true
	}
	return fields[0], fields[1:], true
}

// Directive is one //servo: line in a file: its tokens, and the
// declaration whose doc comment holds it.
type Directive struct {
	Comment *ast.Comment
	Name    string
	Args    []string
	// Func is the function — or method — whose doc comment holds the line,
	// and Type the type declaration's: the TypeSpec's own doc or, for a
	// declaration of exactly one type, the GenDecl's. At most one is set,
	// and neither for a line anywhere else.
	Func *ast.FuncDecl
	Type *ast.TypeSpec
	// typeGroup marks a line in the doc comment of a parenthesized group
	// of type declarations, which documents no single type.
	typeGroup bool
}

// FileDirectives returns every //servo: line in file, in source order, each
// with its attachment. A doc comment is matched back to its declaration by
// pointer identity: go/parser shares one *ast.CommentGroup between
// file.Comments and the declaration's Doc field.
func FileDirectives(file *ast.File) []Directive {
	funcDoc := map[*ast.CommentGroup]*ast.FuncDecl{}
	typeDoc := map[*ast.CommentGroup]*ast.TypeSpec{}
	groupDoc := map[*ast.CommentGroup]bool{}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Doc != nil {
				funcDoc[d.Doc] = d
			}
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, s := range d.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok && ts.Doc != nil {
					typeDoc[ts.Doc] = ts
				}
			}
			if d.Doc == nil {
				continue
			}
			// `type foo struct{...}` hangs its doc comment on the GenDecl,
			// not the TypeSpec; for a parenthesized group the GenDecl's
			// documents the group.
			if len(d.Specs) == 1 {
				if ts, ok := d.Specs[0].(*ast.TypeSpec); ok && ts.Doc == nil {
					typeDoc[d.Doc] = ts
					continue
				}
			}
			groupDoc[d.Doc] = true
		}
	}

	var out []Directive
	for _, group := range file.Comments {
		for _, c := range group.List {
			name, args, ok := ParseDirective(c.Text)
			if !ok {
				continue
			}
			out = append(out, Directive{
				Comment:   c,
				Name:      name,
				Args:      args,
				Func:      funcDoc[group],
				Type:      typeDoc[group],
				typeGroup: groupDoc[group],
			})
		}
	}
	return out
}

// IsRoute reports whether internal/route claims d: a route verb in a
// function's doc comment. A method's counts too — route refuses it there
// with a message about methods, which is more use than one about placement.
func (d Directive) IsRoute() bool {
	return IsRouteVerb(d.Name) && d.Func != nil
}

// IsConfig reports whether ScanConfigs claims d: //servo:config in a type
// declaration's doc comment.
func (d Directive) IsConfig() bool {
	return d.Name == ConfigDirective && d.Type != nil
}

// Unclaimed returns the diagnostic message for a line no reader acts on, or
// "" when IsRoute or IsConfig claims it. These are the lines that used to
// fall between the two scanners — each skipped what looked like the other's
// — so `//servo:confg` on a type, or `//servo:config` on a var, generated
// nothing and said nothing. One owner deciding for every line is what
// closes that gap; internal/route reports the result at generate time and
// servovet in the editor, with the same words.
func (d Directive) Unclaimed() string {
	switch {
	case d.IsRoute(), d.IsConfig():
		return ""
	case d.Name == "":
		return fmt.Sprintf("servo: empty //servo: directive — the prefix is reserved for %s", directiveSummary())
	case IsRouteVerb(d.Name):
		return fmt.Sprintf("servo: //servo:%s must be the doc comment of a top-level function — anywhere else it registers no route", d.Name)
	case d.Name == ConfigDirective && d.typeGroup:
		return fmt.Sprintf("servo: //servo:%s documents a group of type declarations, not one type — move it onto the type it configures", ConfigDirective)
	case d.Name == ConfigDirective:
		return fmt.Sprintf("servo: //servo:%s must be the doc comment of a type declaration — anywhere else no loader is generated", ConfigDirective)
	default:
		return fmt.Sprintf("servo: unknown //servo: directive %q — the prefix is reserved for %s", d.Name, directiveSummary())
	}
}

func directiveSummary() string {
	return fmt.Sprintf("//servo:%s on a type declaration and the route verbs (%s) on a function", ConfigDirective, RouteVerbList)
}
