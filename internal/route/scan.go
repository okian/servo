package route

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// directivePrefix is reserved in full: any comment starting with it that
// does not parse as a valid directive is an error, never a silent no-op —
// a typo'd method must not leave a route unserved.
const directivePrefix = "//servo:"

// methods maps the directive spelling to the ServeMux method token.
var methods = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH",
	"delete": "DELETE", "head": "HEAD", "options": "OPTIONS",
}

const methodList = "get, post, put, patch, delete, head, options"

// bodylessMethods are the methods whose requests the generated decoder
// refuses to read a body (JSON or form) for.
var bodylessMethods = map[string]bool{"GET": true, "HEAD": true, "DELETE": true, "OPTIONS": true}

// Scan walks every main-module package's comments for //servo: directives
// and validates each against its handler, returning the sorted route list
// and every failure as a positioned diagnostic. servoPkg is the loaded
// runtime package, needed to recognize servo.Json by identity.
// servoHTTPTypes bundles the runtime identities the scanner validates
// against: the typed response wrappers (whose type argument is the payload
// schema) and the sealed base interface every response kind implements.
type servoHTTPTypes struct {
	json     *types.TypeName
	xml      *types.TypeName
	response *types.Interface
}

func loadServoHTTPTypes(servoPkg *types.Package) *servoHTTPTypes {
	st := &servoHTTPTypes{}
	st.json, _ = servoPkg.Scope().Lookup("Json").(*types.TypeName)
	st.xml, _ = servoPkg.Scope().Lookup("Xml").(*types.TypeName)
	if tn, ok := servoPkg.Scope().Lookup("Response").(*types.TypeName); ok {
		st.response, _ = tn.Type().Underlying().(*types.Interface)
	}
	if st.json == nil || st.xml == nil || st.response == nil {
		return nil
	}
	return st
}

func Scan(pkgs []*packages.Package, servoPkg *types.Package) ([]*Route, []Diagnostic) {
	st := loadServoHTTPTypes(servoPkg)

	var routes []*Route
	var diags []Diagnostic
	for _, pkg := range pkgs {
		if pkg.Module == nil || !pkg.Module.Main {
			continue
		}
		if pkg.Types == nil || pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			// Directives are just comments: they parse fine against a servo
			// version that predates the HTTP feature, and the CLI can be
			// newer than the module's library. That mismatch must name its
			// fix rather than drop routes or chase a missing type.
			if st == nil {
				if pos, found := firstDirective(pkg, file); found {
					return nil, []Diagnostic{{Pos: pos, Message: "servo: //servo: directives need a servo version that exports the servo.Response family — update github.com/okian/servo/v3 in this module"}}
				}
				continue
			}
			scanFile(pkg, file, st, &routes, &diags)
		}
	}

	// Sorted by (Group, Pattern, Method, Pos): the deterministic order
	// everything downstream — emit, the generated header, duplicate
	// reporting — uses. Group first so each emitted server's routes are
	// contiguous.
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Group != routes[j].Group {
			return routes[i].Group < routes[j].Group
		}
		if routes[i].Pattern != routes[j].Pattern {
			return routes[i].Pattern < routes[j].Pattern
		}
		if routes[i].Method != routes[j].Method {
			return routes[i].Method < routes[j].Method
		}
		return graph.ComparePos(routes[i].Pos, routes[j].Pos) < 0
	})

	routes, dupDiags := dedupAndProbe(routes)
	diags = append(diags, dupDiags...)

	sort.Slice(diags, func(i, j int) bool { return graph.ComparePos(diags[i].Pos, diags[j].Pos) < 0 })
	return routes, diags
}

// CheckDirectiveComment validates one comment line against the //servo:
// grammar (method, pattern, optional group, ServeMux pattern syntax).
// Non-directive lines pass. servo-vet uses this for in-editor feedback;
// the authoritative check — including handler validation and cross-route
// conflicts — runs in Scan.
func CheckDirectiveComment(text string) (problem string, ok bool) {
	if !strings.HasPrefix(text, directivePrefix) {
		return "", true
	}
	if _, diag := parseDirective(text, token.Position{}); diag != nil {
		return diag.Message, false
	}
	return "", true
}

// firstDirective finds the first //servo: line in file, for the
// version-mismatch diagnostic above.
func firstDirective(pkg *packages.Package, file *ast.File) (token.Position, bool) {
	for _, group := range file.Comments {
		for _, c := range group.List {
			if strings.HasPrefix(c.Text, directivePrefix) {
				return pkg.Fset.Position(c.Pos()), true
			}
		}
	}
	return token.Position{}, false
}

// directive is one parsed //servo:<method> <pattern> [group] line.
type directive struct {
	method  string // upper-case
	pattern string
	group   string // "" = default group
	pos     token.Position
}

// groupTokenRE is the directive-token half of the group-name grammar; the
// spec-marker half lives in internal/load and must stay identical.
var groupTokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func scanFile(pkg *packages.Package, file *ast.File, st *servoHTTPTypes, routes *[]*Route, diags *[]Diagnostic) {
	// Doc comment groups are shared pointers between file.Comments and
	// decl.Doc, so a group can be matched back to the function it
	// documents by identity.
	docOf := map[*ast.CommentGroup]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Doc != nil {
			docOf[fd.Doc] = fd
		}
	}

	handled := map[*ast.FuncDecl]bool{}
	for _, group := range file.Comments {
		var claimed []directive
		bad := false
		for _, c := range group.List {
			if !strings.HasPrefix(c.Text, directivePrefix) {
				continue
			}
			pos := pkg.Fset.Position(c.Pos())
			d, diag := parseDirective(c.Text, pos)
			if diag != nil {
				*diags = append(*diags, *diag)
				bad = true
				continue
			}
			claimed = append(claimed, d)
		}
		if len(claimed) == 0 && !bad {
			continue
		}

		fd := docOf[group]
		if fd == nil {
			// Report against each directive line: a floating directive
			// registers nothing, which is exactly the silent failure the
			// reserved prefix exists to prevent.
			for _, d := range claimed {
				*diags = append(*diags, Diagnostic{Pos: d.pos, Message: "servo: //servo: directive must be the doc comment of a top-level function"})
			}
			continue
		}
		if len(claimed) == 0 {
			continue // every line was malformed; already reported
		}
		if handled[fd] {
			continue
		}
		handled[fd] = true

		h, diag := validateHandler(pkg, fd, st, claimed[0].pos)
		if diag != nil {
			*diags = append(*diags, *diag)
			continue
		}
		for _, d := range claimed {
			rt := &Route{
				Method:   d.method,
				Pattern:  d.pattern,
				Group:    d.group,
				Func:     h.fn,
				Pkg:      pkg.PkgPath,
				Name:     pkg.Types.Name() + "." + h.fn.Name(),
				Pos:      d.pos,
				RespType: h.respType,
				Deps:     h.deps,
				DepTypes: h.depTypes,
			}
			if h.reqNamed != nil {
				plan, planDiags := buildReqPlan(h.reqType, h.reqNamed, d)
				*diags = append(*diags, planDiags...)
				if plan == nil {
					continue
				}
				rt.Req = plan
			}
			*routes = append(*routes, rt)
		}
	}
}

// parseDirective parses one claimed comment line. The method must be one of
// the seven verbs and the pattern a single "/"-rooted token.
func parseDirective(text string, pos token.Position) (directive, *Diagnostic) {
	rest := strings.TrimPrefix(text, directivePrefix)
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return directive{}, &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: empty //servo: directive — the //servo: comment prefix is reserved; methods: %s", methodList)}
	}
	method, ok := methods[fields[0]]
	if !ok {
		return directive{}, &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: unknown //servo: directive %q — the //servo: comment prefix is reserved; methods: %s", fields[0], methodList)}
	}
	if len(fields) < 2 {
		return directive{}, &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: //servo:%s needs a route pattern, e.g. //servo:%s /order/{category}/", fields[0], fields[0])}
	}
	if !strings.HasPrefix(fields[1], "/") {
		return directive{}, &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: route pattern %q must start with %q", fields[1], "/")}
	}
	if len(fields) > 3 {
		return directive{}, &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: //servo:%s takes a pattern and an optional group — unexpected %q after %q", fields[0], fields[3], fields[2])}
	}
	group := ""
	if len(fields) == 3 {
		if !groupTokenRE.MatchString(fields[2]) {
			return directive{}, &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: group %q must match [A-Za-z0-9_-]+", fields[2])}
		}
		if fields[2] != "default" {
			group = fields[2]
		}
	}
	if diag := probePattern(method, fields[1], pos); diag != nil {
		return directive{}, diag
	}
	return directive{method: method, pattern: fields[1], group: group, pos: pos}, nil
}

// probePattern registers the single pattern on a fresh ServeMux: a panic
// from a lone registration can only be a syntax error, reported with
// net/http's own words. Conflicts between routes are a separate probe over
// the full set in dedupAndProbe.
func probePattern(method, pattern string, pos token.Position) (diag *Diagnostic) {
	defer func() {
		if r := recover(); r != nil {
			diag = &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: route %s %s cannot be registered on net/http.ServeMux: %v", method, pattern, r)}
		}
	}()
	http.NewServeMux().Handle(method+" "+pattern, http.NotFoundHandler())
	return nil
}

// handlerInfo is a validated handler's shape, shared by every directive
// line above it.
type handlerInfo struct {
	fn       *types.Func
	reqType  types.Type   // *ReqT, nil when no request struct
	reqNamed *types.Named // ReqT
	respType types.Type
	deps     []graph.Key
	depTypes []types.Type
}

// validateHandler checks the fixed signature contract:
//
//	func Name(ctx context.Context [, req *ReqT] [, deps...]) (servo.Json[T], error)
//
// Param 2 is the request struct iff it is a pointer to a named struct that
// declares at least one binding tag; everything else after ctx is a
// dependency.
func validateHandler(pkg *packages.Package, fd *ast.FuncDecl, st *servoHTTPTypes, pos token.Position) (*handlerInfo, *Diagnostic) {
	if fd.Recv != nil {
		return nil, &Diagnostic{Pos: pos, Message: "servo: //servo: handlers must be top-level functions, not methods"}
	}
	fn, _ := pkg.TypesInfo.Defs[fd.Name].(*types.Func)
	if fn == nil {
		return nil, &Diagnostic{Pos: pos, Message: "servo: //servo: directive must be the doc comment of a top-level function"}
	}
	name := pkg.Types.Name() + "." + fn.Name()
	sig := fn.Type().(*types.Signature)

	if !fn.Exported() {
		return nil, &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: //servo: handler %s must be exported — the generated server lives in the injector package and has to call it", name)}
	}
	if sig.TypeParams().Len() > 0 {
		return nil, &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: //servo: handler %s must not be generic", name)}
	}
	if sig.Variadic() {
		return nil, &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: //servo: handler %s must not be variadic", name)}
	}

	params := sig.Params()
	if params.Len() == 0 || !isContextType(params.At(0).Type()) {
		return nil, &Diagnostic{Pos: pos, Message: fmt.Sprintf("servo: //servo: handler %s's first parameter must be context.Context", name)}
	}

	respType, diag := checkResults(sig, st, name, pos)
	if diag != nil {
		return nil, diag
	}

	h := &handlerInfo{fn: fn, respType: respType}
	depStart := 1
	if params.Len() > 1 {
		if named, ok := requestStruct(params.At(1).Type()); ok {
			h.reqType = params.At(1).Type()
			h.reqNamed = named
			depStart = 2
		}
	}
	for i := depStart; i < params.Len(); i++ {
		pt := params.At(i).Type()
		h.deps = append(h.deps, graph.NewKey(pt, ""))
		h.depTypes = append(h.depTypes, pt)
	}
	return h, nil
}

// checkResults validates the result tuple and, for the typed wrappers,
// extracts the payload type — the response schema the signature carries.
// Any member of the sealed family passes: the check is "implements
// servo.Response", so a new response kind in the runtime needs no scanner
// change.
func checkResults(sig *types.Signature, st *servoHTTPTypes, name string, pos token.Position) (types.Type, *Diagnostic) {
	shape := func(detail string) *Diagnostic {
		msg := fmt.Sprintf("servo: //servo: handler %s must return (R, error) where R is a servo response type (servo.Json[T], servo.Xml[T] or servo.Response)", name)
		if detail != "" {
			msg += " — " + detail
		}
		return &Diagnostic{Pos: pos, Message: msg}
	}

	res := sig.Results()
	if res.Len() != 2 {
		return nil, shape("")
	}
	second := res.At(1).Type()
	if !types.Identical(second, types.Universe.Lookup("error").Type()) {
		if types.Implements(second, types.Universe.Lookup("error").Type().Underlying().(*types.Interface)) {
			return nil, shape(fmt.Sprintf("the second result is %s, which implements error but is not the error interface itself", graph.TypeString(second)))
		}
		return nil, shape("")
	}
	first := types.Unalias(res.At(0).Type())
	if !types.Implements(first, st.response) {
		return nil, shape("")
	}
	// The typed wrappers carry the payload type; bare Response (or any
	// interface embedding it) carries none.
	if named, ok := first.(*types.Named); ok && (named.Obj() == st.json || named.Obj() == st.xml) {
		if targs := named.TypeArgs(); targs != nil && targs.Len() == 1 {
			return targs.At(0), nil
		}
	}
	return nil, nil
}

func isContextType(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "context" && obj.Name() == "Context"
}

// bindingTags are the four explicit source tags; json marks a body field.
var bindingTags = []string{"path", "query", "header", "form"}

// requestStruct reports whether t is a pointer to a named struct that
// declares at least one binding or json tag — the shape that makes param 2
// a request struct rather than a dependency.
func requestStruct(t types.Type) (*types.Named, bool) {
	ptr, ok := types.Unalias(t).(*types.Pointer)
	if !ok {
		return nil, false
	}
	named, ok := types.Unalias(ptr.Elem()).(*types.Named)
	if !ok {
		return nil, false
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil, false
	}
	for i := 0; i < st.NumFields(); i++ {
		tag := reflect.StructTag(st.Tag(i))
		for _, key := range bindingTags {
			if _, ok := tag.Lookup(key); ok {
				return named, true
			}
		}
		if _, ok := tag.Lookup("json"); ok {
			return named, true
		}
	}
	return nil, false
}

// buildReqPlan validates one request struct against one directive and
// produces the binding plan. It returns nil (with diagnostics) when the
// struct is unusable; per-field problems that don't invalidate the whole
// plan still fail the scan via their diagnostics.
func buildReqPlan(reqType types.Type, named *types.Named, d directive) (*ReqPlan, []Diagnostic) {
	var diags []Diagnostic
	structName := graph.TypeString(named)

	if !named.Obj().Exported() {
		return nil, []Diagnostic{{Pos: d.pos, Message: fmt.Sprintf("servo: request struct type %s must be exported — the generated server has to construct one", structName)}}
	}

	plan := &ReqPlan{Type: reqType, Named: named}
	st := named.Underlying().(*types.Struct)
	// boundBy tracks tag values per kind so two fields claiming the same
	// source name are caught here rather than silently last-write-wins in
	// the generated decoder.
	boundBy := map[BindKind]map[string]string{}

	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		tag := reflect.StructTag(st.Tag(i))

		var kinds []BindKind
		var params []string
		for _, key := range bindingTags {
			if v, ok := tag.Lookup(key); ok {
				kinds = append(kinds, kindOfTag(key))
				params = append(params, v)
			}
		}

		if !f.Exported() {
			if len(kinds) > 0 {
				diags = append(diags, Diagnostic{Pos: d.pos, Message: fmt.Sprintf("servo: field %s of %s is unexported but carries a binding tag — the generated decoder cannot assign it", f.Name(), structName)})
			}
			continue
		}
		if len(kinds) > 1 {
			names := make([]string, len(kinds))
			for i, k := range kinds {
				names[i] = k.String()
			}
			diags = append(diags, Diagnostic{Pos: d.pos, Message: fmt.Sprintf("servo: field %s of %s has more than one binding tag (%s) — a field binds from exactly one source", f.Name(), structName, strings.Join(names, ", "))})
			continue
		}

		if len(kinds) == 0 {
			jsonName, ok := tag.Lookup("json")
			effective := f.Name()
			if ok {
				base, _, _ := strings.Cut(jsonName, ",")
				if base == "-" {
					continue
				}
				if base != "" {
					effective = base
				}
			}
			plan.Fields = append(plan.Fields, Field{Name: f.Name(), Kind: BindBody, Param: effective, Type: f.Type()})
			plan.HasBody = true
			continue
		}

		kind, param := kinds[0], params[0]
		if !isScalar(f.Type()) {
			diags = append(diags, Diagnostic{Pos: d.pos, Message: fmt.Sprintf("servo: field %s of %s: %s-bound fields must be a scalar (string, bool, int/uint families, float32/64), got %s", f.Name(), structName, kind, graph.TypeString(f.Type()))})
			continue
		}
		if prior, dup := boundBy[kind][param]; dup {
			diags = append(diags, Diagnostic{Pos: d.pos, Message: fmt.Sprintf("servo: fields %s and %s of %s both bind %s %q", prior, f.Name(), structName, kind, param)})
			continue
		}
		if boundBy[kind] == nil {
			boundBy[kind] = map[string]string{}
		}
		boundBy[kind][param] = f.Name()
		plan.Fields = append(plan.Fields, Field{Name: f.Name(), Kind: kind, Param: param, Type: f.Type()})
		if kind == BindForm {
			plan.HasForm = true
		}
	}

	if plan.HasBody && plan.HasForm {
		diags = append(diags, Diagnostic{Pos: d.pos, Message: fmt.Sprintf("servo: request struct %s mixes form fields with JSON body fields — a request body is either a form or JSON, not both", structName)})
	}
	if bodylessMethods[d.method] {
		if plan.HasBody {
			diags = append(diags, Diagnostic{Pos: d.pos, Message: fmt.Sprintf("servo: %s %s: request struct %s declares JSON body fields, but %s requests carry no body", d.method, d.pattern, structName, d.method)})
		}
		if plan.HasForm {
			diags = append(diags, Diagnostic{Pos: d.pos, Message: fmt.Sprintf("servo: %s %s: request struct %s declares form fields, but %s requests carry no body", d.method, d.pattern, structName, d.method)})
		}
	}

	// Cross-check path tags against the pattern, both directions: a tag
	// with no segment reads an empty string forever, a segment with no tag
	// is routing data the handler silently never sees.
	segments := wildcards(d.pattern)
	for _, f := range plan.Fields {
		if f.Kind != BindPath {
			continue
		}
		if !segments[f.Param] {
			diags = append(diags, Diagnostic{Pos: d.pos, Message: fmt.Sprintf("servo: field %s of %s binds path %q, but the pattern %q has no {%s} segment", f.Name, structName, f.Param, d.pattern, f.Param)})
		}
	}
	for seg := range segments {
		if seg == "$" {
			continue
		}
		found := false
		for _, f := range plan.Fields {
			if f.Kind == BindPath && f.Param == seg {
				found = true
				break
			}
		}
		if !found {
			diags = append(diags, Diagnostic{Pos: d.pos, Message: fmt.Sprintf("servo: pattern segment {%s} of route %s %s has no path:%q field in %s", seg, d.method, d.pattern, seg, structName)})
		}
	}

	if len(diags) > 0 {
		return nil, diags
	}
	return plan, nil
}

func kindOfTag(key string) BindKind {
	switch key {
	case "path":
		return BindPath
	case "query":
		return BindQuery
	case "header":
		return BindHeader
	default:
		return BindForm
	}
}

// isScalar reports whether t is one of the types the generated decoder can
// parse from a string: after alias resolution, a basic string, bool,
// signed/unsigned integer (except uintptr) or float.
func isScalar(t types.Type) bool {
	basic, ok := types.Unalias(t).(*types.Basic)
	if !ok {
		return false
	}
	switch basic.Kind() {
	case types.String, types.Bool,
		types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64,
		types.Float32, types.Float64:
		return true
	default:
		return false
	}
}

// wildcards extracts the {name} segments of a ServeMux pattern, with any
// "..." suffix stripped. "{$}" comes back as "$", which callers treat as
// structural rather than bindable. A malformed pattern simply yields fewer
// names — the ServeMux probe below reports it with net/http's own words.
func wildcards(pattern string) map[string]bool {
	out := map[string]bool{}
	for seg := range strings.SplitSeq(pattern, "/") {
		if len(seg) < 3 || seg[0] != '{' || seg[len(seg)-1] != '}' {
			continue
		}
		name := strings.TrimSuffix(seg[1:len(seg)-1], "...")
		if name != "" {
			out[name] = true
		}
	}
	return out
}

// dedupAndProbe drops exact duplicate (method, pattern) routes with a
// both-positions diagnostic, then registers every survivor on a scratch
// ServeMux: net/http's own registration is the authority on pattern syntax
// and wildcard conflicts, so generate fails exactly when App startup would
// panic, with the same message and zero rules to maintain here.
func dedupAndProbe(routes []*Route) ([]*Route, []Diagnostic) {
	var diags []Diagnostic
	seen := map[string]*Route{}
	kept := routes[:0]
	for _, rt := range routes {
		// Keyed per group: two groups are two servers with two muxes, so
		// the same method+pattern across them is legitimate.
		key := rt.Group + "\x00" + rt.Method + " " + rt.Pattern
		if prior := seen[key]; prior != nil {
			diags = append(diags, Diagnostic{Pos: rt.Pos, Message: fmt.Sprintf("servo: duplicate route %s %s — first declared at %s", rt.Method, rt.Pattern, prior.Pos)})
			continue
		}
		seen[key] = rt
		kept = append(kept, rt)
	}

	muxes := map[string]*http.ServeMux{}
	for _, rt := range kept {
		mux := muxes[rt.Group]
		if mux == nil {
			mux = http.NewServeMux()
			muxes[rt.Group] = mux
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					diags = append(diags, Diagnostic{Pos: rt.Pos, Message: fmt.Sprintf("servo: route %s %s cannot be registered on net/http.ServeMux: %v", rt.Method, rt.Pattern, r)})
				}
			}()
			mux.Handle(rt.Method+" "+rt.Pattern, http.NotFoundHandler())
		}()
	}
	return kept, diags
}
