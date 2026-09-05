// Package route scans the main module for //servo: HTTP directives and
// turns them into a typed route model. It is module-wide and
// injector-agnostic — like capability loading, and unlike the constructor
// scan — because a directive names an exported handler that any injector
// declaring servo.HTTP() may serve.
package route

import (
	"fmt"
	"go/token"
	"go/types"

	"github.com/okian/servo/v3/internal/graph"
)

// BindKind is where one request-struct field's value comes from.
type BindKind int

const (
	// BindPath reads r.PathValue(param) — a `path:"x"` tag.
	BindPath BindKind = iota
	// BindQuery reads the URL query — a `query:"x"` tag.
	BindQuery
	// BindHeader reads a request header — a `header:"X-Foo"` tag.
	BindHeader
	// BindForm reads a urlencoded or multipart form value — a `form:"x"` tag.
	BindForm
	// BindBody is decoded from the JSON request body — a `json:"x"` tag or
	// an untagged exported field.
	BindBody
)

func (k BindKind) String() string {
	switch k {
	case BindPath:
		return "path"
	case BindQuery:
		return "query"
	case BindHeader:
		return "header"
	case BindForm:
		return "form"
	case BindBody:
		return "body"
	default:
		return "unknown"
	}
}

// Field is one bound field of a request struct.
type Field struct {
	// Name is the Go field name the generated decoder assigns to.
	Name string
	Kind BindKind
	// Param is the source name: the wildcard, query key, header name or
	// form key — for BindBody, the effective JSON name (documentation
	// only; encoding/json does the actual matching).
	Param string
	// Type is the field's declared type. Scalar for every kind except
	// BindBody, which may be anything encoding/json can decode.
	Type types.Type
}

// ReqPlan is the decoded shape of a handler's request struct.
type ReqPlan struct {
	// Type is the parameter's type as declared: a pointer to a named
	// struct.
	Type types.Type
	// Named is Type's element, kept so emit can name the struct without
	// re-unwrapping.
	Named *types.Named
	// Fields lists the bound fields in declaration order.
	Fields []Field
	// HasBody reports whether any field decodes from the JSON body.
	HasBody bool
	// HasForm reports whether any field binds from a form.
	HasForm bool
}

// Route is one //servo: directive resolved against its handler.
type Route struct {
	// Method is upper-case: "POST".
	Method string
	// Pattern is the ServeMux pattern as written, without the method.
	Pattern string
	// Group is the directive's trailing token, "" for the default group
	// (a literal "default" token normalizes to "" at scan time).
	Group string
	// Func is the handler.
	Func *types.Func
	// Pkg is the handler's import path.
	Pkg string
	// Name is "pkgname.Func", matching graph.Provider.Name's style.
	Name string
	// Pos is the directive comment's position.
	Pos token.Position
	// Req is nil when the handler takes no request struct.
	Req *ReqPlan
	// RespType is T in the declared servo.Json[T] result.
	RespType types.Type
	// Deps are the graph keys of every parameter after ctx (and the
	// request struct, when present), in declaration order; DepTypes is
	// the parallel type list.
	Deps     []graph.Key
	DepTypes []types.Type
}

// Diagnostic is one scan failure, in the same shape as resolve.Diagnostic:
// a position plus a message that may carry its own multi-line detail.
type Diagnostic struct {
	Pos     token.Position
	Message string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s: %s", d.Pos, d.Message)
}

func (d Diagnostic) Error() string { return d.String() }

// HTTPConfigKey returns the graph key and type for *servo.HTTPConfig — the
// node the emitted server reads its listen address, TLS files and limits
// from. It lives here so resolve and the pipeline agree on one spelling.
func HTTPConfigKey(servoPkg *types.Package) (graph.Key, types.Type) {
	obj := servoPkg.Scope().Lookup("HTTPConfig")
	tn, ok := obj.(*types.TypeName)
	if !ok {
		panic("servo package has no HTTPConfig type — internal version mismatch")
	}
	ptr := types.NewPointer(tn.Type())
	return graph.NewKey(ptr, ""), ptr
}
