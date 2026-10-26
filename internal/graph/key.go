// Package graph defines the resolved-graph data model: canonical type
// identity, provider candidates, and structural capability detection.
package graph

import "go/types"

// Key is the canonical identity of a value in the object graph: a fully
// qualified type string (with every alias resolved, at any depth, so
// aliases collapse to their target — byte and rune to uint8 and int32)
// plus an optional tag. T and *T are distinct keys because their type
// strings differ by the leading "*".
type Key struct {
	Type string
	Tag  string
}

func (k Key) String() string {
	if k.Tag == "" {
		return k.Type
	}
	return k.Type + "#" + k.Tag
}

// NewKey builds the canonical Key for t. Passing a non-empty tag
// distinguishes multiple bindings of the same type.
func NewKey(t types.Type, tag string) Key {
	return Key{Type: TypeString(t), Tag: tag}
}

// TypeString renders t with full import paths (never a bare local name and
// never abbreviated), after resolving aliases, so it is stable across
// packages and safe to use as a map key or diagnostic identifier.
func TypeString(t types.Type) string {
	return types.TypeString(unaliasDeep(t), fullPathQualifier)
}

// TypeStringWith is TypeString with the caller's qualifier — package names
// as a spec file writes them, say — and the same deep alias resolution, so
// what a diagnostic suggests names the type its key does.
func TypeStringWith(t types.Type, q types.Qualifier) string {
	return types.TypeString(unaliasDeep(t), q)
}

// unaliasDeep resolves every alias in t, wherever it sits: t's own alias
// chain, and any alias nested inside it — a pointer's element, a type
// argument, a slice, array, map or channel element, a function's
// parameters and results, a struct's fields, an interface's methods.
// types.Unalias resolves only its argument, not the types inside it, so
// *Alias and *Underlying, or Cache[DBAlias] and Cache[*DB], rendered as two
// strings for one type. types.Identical agrees they are the same type; only
// the rendered key disagreed, and a constructor for one then never
// satisfied a parameter written as the other.
//
// byte and rune are aliases too, of uint8 and int32 as the spec defines
// them, but go/types keeps each as a type of its own carrying the spelling
// the source used, which types.Unalias does not touch: []byte and []uint8
// rendered as two keys the same way. They resolve to uint8 and int32 here.
//
// A subtree holding no alias comes back as the identical object, so the
// common case rebuilds nothing.
func unaliasDeep(t types.Type) types.Type {
	switch u := types.Unalias(t).(type) {
	case *types.Pointer:
		if elem := unaliasDeep(u.Elem()); elem != u.Elem() {
			return types.NewPointer(elem)
		}
		return u
	case *types.Slice:
		if elem := unaliasDeep(u.Elem()); elem != u.Elem() {
			return types.NewSlice(elem)
		}
		return u
	case *types.Array:
		if elem := unaliasDeep(u.Elem()); elem != u.Elem() {
			return types.NewArray(elem, u.Len())
		}
		return u
	case *types.Chan:
		if elem := unaliasDeep(u.Elem()); elem != u.Elem() {
			return types.NewChan(u.Dir(), elem)
		}
		return u
	case *types.Map:
		key, elem := unaliasDeep(u.Key()), unaliasDeep(u.Elem())
		if key != u.Key() || elem != u.Elem() {
			return types.NewMap(key, elem)
		}
		return u
	case *types.Named:
		return unaliasTypeArgs(u)
	case *types.Signature:
		params, paramsChanged := unaliasTuple(u.Params())
		results, resultsChanged := unaliasTuple(u.Results())
		if paramsChanged || resultsChanged {
			return types.NewSignatureType(nil, nil, nil, params, results, u.Variadic())
		}
		return u
	case *types.Struct:
		return unaliasFields(u)
	case *types.Interface:
		if u.Empty() {
			// The one alias kept on purpose. any and interface{} are one
			// type, so they need one spelling, and "any" is the one Go code
			// writes: it keeps node names — which reach Report entries and
			// servotest assertions — what they were before aliases were
			// resolved at every depth, rather than renaming every Repo[any]
			// to Repo[interface{}].
			return universeAny
		}
		return unaliasMethods(u)
	case *types.Basic:
		// Typ holds the one canonical object per kind; byte and rune are
		// the predeclared types that are not it.
		return types.Typ[u.Kind()]
	default:
		return u
	}
}

// universeAny is the predeclared any, which types.TypeString renders as
// "any": the canonical spelling of the empty interface in a key.
var universeAny = types.Universe.Lookup("any").Type()

// unaliasTypeArgs re-instantiates n's generic origin with its type
// arguments resolved, so Cache[DBAlias] and Cache[*DB] render alike. A
// named type's underlying type is never walked: the name is the identity,
// and the type arguments are all that can differ between two spellings.
func unaliasTypeArgs(n *types.Named) types.Type {
	targs := n.TypeArgs()
	if targs.Len() == 0 {
		return n
	}
	args := make([]types.Type, targs.Len())
	changed := false
	for i := range args {
		args[i] = unaliasDeep(targs.At(i))
		changed = changed || args[i] != targs.At(i)
	}
	if !changed {
		return n
	}
	// Without validation the only possible failure is an arity mismatch,
	// which arguments read off an instance of this very origin cannot have.
	inst, err := types.Instantiate(nil, n.Origin(), args, false)
	if err != nil {
		return n
	}
	return inst
}

func unaliasTuple(tuple *types.Tuple) (*types.Tuple, bool) {
	if tuple.Len() == 0 {
		return tuple, false
	}
	vars := make([]*types.Var, tuple.Len())
	changed := false
	for i := range vars {
		v := tuple.At(i)
		typ := unaliasDeep(v.Type())
		changed = changed || typ != v.Type()
		vars[i] = types.NewParam(v.Pos(), v.Pkg(), v.Name(), typ)
	}
	if !changed {
		return tuple, false
	}
	return types.NewTuple(vars...), true
}

func unaliasFields(s *types.Struct) types.Type {
	fields := make([]*types.Var, s.NumFields())
	tags := make([]string, s.NumFields())
	changed := false
	for i := range fields {
		f := s.Field(i)
		typ := f.Type()
		// An embedded field is named after the type written for it, so
		// struct{ Alias } and struct{ Target } are different types — the
		// field is called Alias in one and Target in the other — and so are
		// struct{ byte } and struct{ uint8 }. Resolving the alias here
		// would make the two render alike. Its type arguments are another
		// matter: struct{ Cache[DBAlias] } and struct{ Cache[*DB] } both
		// have a field named Cache, of one type, and need one key.
		if f.Embedded() {
			typ = unaliasEmbedded(typ)
		} else {
			typ = unaliasDeep(typ)
		}
		changed = changed || typ != f.Type()
		fields[i] = types.NewField(f.Pos(), f.Pkg(), f.Name(), typ, f.Embedded())
		tags[i] = s.Tag(i)
	}
	if !changed {
		return s
	}
	return types.NewStruct(fields, tags)
}

// unaliasEmbedded resolves an embedded field's type as far as its name
// allows: a generic type's arguments, written T[A] or *T[A], while T, the
// field's name, stays as written.
func unaliasEmbedded(t types.Type) types.Type {
	switch u := t.(type) {
	case *types.Pointer:
		if elem := unaliasEmbedded(u.Elem()); elem != u.Elem() {
			return types.NewPointer(elem)
		}
		return u
	case *types.Named:
		return unaliasTypeArgs(u)
	default:
		return u
	}
}

func unaliasMethods(iface *types.Interface) types.Type {
	methods := make([]*types.Func, iface.NumExplicitMethods())
	changed := false
	for i := range methods {
		m := iface.ExplicitMethod(i)
		sig := m.Type().(*types.Signature)
		params, paramsChanged := unaliasTuple(sig.Params())
		results, resultsChanged := unaliasTuple(sig.Results())
		changed = changed || paramsChanged || resultsChanged
		// The receiver is left unset: NewInterfaceType fills it in.
		methods[i] = types.NewFunc(m.Pos(), m.Pkg(), m.Name(), types.NewSignatureType(nil, nil, nil, params, results, sig.Variadic()))
	}
	embeddeds := make([]types.Type, iface.NumEmbeddeds())
	for i := range embeddeds {
		embeddeds[i] = unaliasDeep(iface.EmbeddedType(i))
		changed = changed || embeddeds[i] != iface.EmbeddedType(i)
	}
	if !changed {
		return iface
	}
	return types.NewInterfaceType(methods, embeddeds).Complete()
}

func fullPathQualifier(pkg *types.Package) string {
	return pkg.Path()
}
