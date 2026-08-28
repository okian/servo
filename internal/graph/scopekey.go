package graph

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// ScopeKeyMethodName is the method that marks a type as scoped. It is
// found by name rather than by types.Implements, unlike the seven
// capability interfaces: its dependency list varies per type, so there is
// no single interface shape to match against.
const ScopeKeyMethodName = "ScopeKey"

// ScopeKeyError is a ScopeKey method that cannot be honoured. It carries
// its own position rather than baking one into the message, so a caller
// building a Diagnostic renders the position once instead of twice.
type ScopeKeyError struct {
	Pos token.Position
	Msg string
}

func (e *ScopeKeyError) Error() string { return e.Pos.String() + ": " + e.Msg }

// ScopeKey is one detected, validated key extractor.
type ScopeKey struct {
	Func *types.Func
	// Owner is the node type the method was found on, exactly as a
	// provider returns it (*chat.Room, not chat.Room).
	Owner     Key
	OwnerType types.Type
	// KeyType is K in (K, error). Scope identity is this type's identity:
	// two scopes returning the same defined type are one scope.
	KeyType types.Type
	KeyKey  Key
	// Params and ParamTypes are the extractor's dependencies — everything
	// after the leading context.Context. They resolve as ordinary graph
	// edges and must be singletons, since the extractor runs before any
	// instance exists.
	Params     []Key
	ParamTypes []types.Type
	Pos        token.Position
}

// ScopeKeyShaped reports whether t declares a ScopeKey method that is
// plausibly a key extractor at all: context first, exactly (K, error) out,
// K a defined non-interface type.
//
// It exists because `ScopeKey` is an ordinary method name that any package
// may already use for something else. FindScopeKey is strict — it reports
// every way a declared scope's extractor can be malformed — and applying
// that strictness to every node in every graph would make an unrelated
// method somewhere in a dependency fail generation for a module with no
// scopes at all. Callers that are merely *asking* whether a type is scoped
// gate on this first; callers acting on a servo.Scoped declaration do not,
// because there the user has said this type is scoped and deserves the
// specific reason it cannot be.
func ScopeKeyShaped(t types.Type) bool {
	m := scopeKeyMethod(t)
	if m == nil {
		return false
	}
	sig, ok := m.Type().(*types.Signature)
	if !ok {
		return false
	}
	if sig.Params().Len() == 0 || !IsContextType(sig.Params().At(0).Type()) {
		return false
	}
	if sig.Results().Len() != 2 || !types.Identical(sig.Results().At(1).Type(), errorType) {
		return false
	}
	named, ok := types.Unalias(sig.Results().At(0).Type()).(*types.Named)
	if !ok {
		return false
	}
	_, isIface := named.Underlying().(*types.Interface)
	return !isIface
}

// ScopeKeyPos is the declaration position of t's ScopeKey method, for a
// caller that needs to point at it without insisting the method be valid.
func ScopeKeyPos(fset *token.FileSet, t types.Type) token.Position {
	if m := scopeKeyMethod(t); m != nil {
		return fset.Position(m.Pos())
	}
	return token.Position{}
}

// ScopeKeyLikely reports whether t declares a ScopeKey method that is
// *trying* to be a key extractor: the right name and a leading
// context.Context. It is deliberately looser than ScopeKeyShaped, which
// also requires the (K, error) results.
//
// The looseness is the point. The single most dangerous mistake this
// feature has is an extractor that forgot its error result — a missing key
// then becomes the zero K, and every keyless caller silently shares one
// instance. Recognizing by name-plus-context and reporting the specific
// defect catches that; recognizing by full shape would let it through as
// an ordinary singleton with nothing said.
func ScopeKeyLikely(t types.Type) bool {
	m := scopeKeyMethod(t)
	if m == nil {
		return false
	}
	sig, ok := m.Type().(*types.Signature)
	return ok && sig.Params().Len() > 0 && IsContextType(sig.Params().At(0).Type())
}

// scopeKeyMethod returns the ScopeKey method declared directly on t's
// named type, or nil.
func scopeKeyMethod(t types.Type) *types.Func {
	named := unwrapNamed(t)
	if named == nil {
		return nil
	}
	for i := 0; i < named.NumMethods(); i++ {
		if named.Method(i).Name() == ScopeKeyMethodName {
			return named.Method(i)
		}
	}
	return nil
}

// PromotedScopeKey reports whether t reaches a ScopeKey through an
// embedded field rather than declaring one. Such a method is deliberately
// not usable as an extractor — calling it on a typed nil would dereference
// the embedded field — but saying "has no ScopeKey method" about a type
// where `x.ScopeKey(ctx)` compiles is not a useful thing to be told.
func PromotedScopeKey(t types.Type) bool {
	if scopeKeyMethod(t) != nil {
		return false
	}
	ptr := types.Unalias(t)
	if _, isPtr := ptr.(*types.Pointer); !isPtr {
		ptr = types.NewPointer(ptr)
	}
	mset := types.NewMethodSet(ptr)
	return mset.Lookup(nil, ScopeKeyMethodName) != nil
}

// FindScopeKey looks for a ScopeKey method declared directly on t's named
// type. It returns (nil, nil) when there is none — the overwhelmingly
// common case, since almost no type is scoped — and a non-nil error when
// there is one whose shape cannot be honoured.
//
// Promoted methods are deliberately not considered. A ScopeKey reached
// through an embedded field would be called on a typed nil whose embedded
// field is itself nil, turning the one thing the blank-receiver rule
// exists to prevent back into a production panic.
func FindScopeKey(fset *token.FileSet, t types.Type) (*ScopeKey, error) {
	m := scopeKeyMethod(t)
	if m == nil {
		return nil, nil
	}

	pos := fset.Position(m.Pos())
	sig, ok := m.Type().(*types.Signature)
	if !ok {
		return nil, &ScopeKeyError{pos, fmt.Sprintf("%s is not a method", ScopeKeyMethodName)}
	}

	if _, isPtr := types.Unalias(t).(*types.Pointer); isPtr {
		if _, recvIsPtr := types.Unalias(sig.Recv().Type()).(*types.Pointer); !recvIsPtr {
			return nil, &ScopeKeyError{pos, fmt.Sprintf(
				"%s must be declared on the pointer receiver, because %s is the type in the graph — generated code calls it on a typed nil, and a value receiver would dereference that nil",
				ScopeKeyMethodName, TypeString(t))}
		}
	}

	if sig.Variadic() {
		return nil, &ScopeKeyError{pos, fmt.Sprintf(
			"%s must not be variadic — every parameter after ctx is resolved as a dependency, and a variadic one is a slice, which is never resolvable",
			ScopeKeyMethodName)}
	}
	if sig.Params().Len() == 0 || !IsContextType(sig.Params().At(0).Type()) {
		return nil, &ScopeKeyError{pos, fmt.Sprintf(
			"%s's first parameter must be context.Context — the scope key comes from the request context",
			ScopeKeyMethodName)}
	}
	if sig.Results().Len() != 2 || !types.Identical(sig.Results().At(1).Type(), errorType) {
		return nil, &ScopeKeyError{pos, fmt.Sprintf(
			"%s must return exactly (K, error) — without the error a missing key becomes the zero K, and every keyless caller silently shares one instance",
			ScopeKeyMethodName)}
	}

	keyType := sig.Results().At(0).Type()
	if err := validKeyType(pos, keyType); err != nil {
		return nil, err
	}

	params := make([]Key, 0, sig.Params().Len()-1)
	paramTypes := make([]types.Type, 0, sig.Params().Len()-1)
	for i := 1; i < sig.Params().Len(); i++ {
		pt := sig.Params().At(i).Type()
		params = append(params, NewKey(pt, ""))
		paramTypes = append(paramTypes, pt)
	}

	return &ScopeKey{
		Func:       m,
		Owner:      NewKey(t, ""),
		OwnerType:  t,
		KeyType:    keyType,
		KeyKey:     NewKey(keyType, ""),
		Params:     params,
		ParamTypes: paramTypes,
		Pos:        pos,
	}, nil
}

// validKeyType enforces that K is a defined, comparable, non-interface
// type. Scope identity *is* type identity: if two unrelated scopes both
// returned string, nothing in the generator could tell whether they share
// a scope or merely share a representation.
func validKeyType(pos token.Position, k types.Type) error {
	named, ok := types.Unalias(k).(*types.Named)
	if !ok {
		return &ScopeKeyError{pos, fmt.Sprintf(
			"%s's key type is %s, which is not a defined type — scope identity is type identity, so declare `type RoomKey %s` and return that instead",
			ScopeKeyMethodName, TypeString(k), TypeString(k))}
	}
	if _, isIface := named.Underlying().(*types.Interface); isIface {
		return &ScopeKeyError{pos, fmt.Sprintf(
			"%s's key type %s is an interface — two callers holding different dynamic types would compare unequal and never share an instance",
			ScopeKeyMethodName, TypeString(k))}
	}
	if !types.Comparable(k) {
		return &ScopeKeyError{pos, fmt.Sprintf(
			"%s's key type %s is not comparable, so it cannot key the scope's instance map",
			ScopeKeyMethodName, TypeString(k))}
	}
	return nil
}

// IsContextType reports whether t is context.Context, checked by package
// path and name rather than by object identity, so callers don't have to
// thread a loaded context package through every check.
func IsContextType(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "context" && obj.Name() == "Context"
}

func unwrapNamed(t types.Type) *types.Named {
	switch u := types.Unalias(t).(type) {
	case *types.Named:
		return u
	case *types.Pointer:
		return unwrapNamed(u.Elem())
	default:
		return nil
	}
}

// FuncDeclOf finds the declaration of fn among pkgs' syntax, or nil when
// the declaring package was loaded without syntax (stdlib and
// third-party dependencies, depending on the load mode).
func FuncDeclOf(pkgs []*packages.Package, fn *types.Func) *ast.FuncDecl {
	for _, pkg := range pkgs {
		if pkg.Types == nil || fn.Pkg() == nil || pkg.PkgPath != fn.Pkg().Path() {
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Name == nil {
					continue
				}
				if pkg.TypesInfo != nil && pkg.TypesInfo.Defs[fd.Name] == fn {
					return fd
				}
			}
		}
	}
	return nil
}

// ReceiverIsBlank reports whether decl's receiver cannot be referenced
// from its body — either left unnamed entirely, which is the form servo
// recommends and staticcheck's ST1006 asks for, or written as `_`, which
// is equally safe and equally accepted.
//
// This is what makes calling ScopeKey on a typed nil safe, and the type
// system has no way to express it: the check is the same trade already
// made for servo-vet's marker-call rule, converting a nil-pointer panic in
// production into a diagnostic at generate time.
func ReceiverIsBlank(decl *ast.FuncDecl) bool {
	if decl == nil || decl.Recv == nil || len(decl.Recv.List) != 1 {
		return false
	}
	names := decl.Recv.List[0].Names
	switch len(names) {
	case 0:
		return true // func (*Room) ScopeKey(...) — nothing to reference
	case 1:
		return names[0].Name == "_"
	default:
		return false
	}
}
