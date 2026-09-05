package graph

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
)

// ConfigDirective is the directive name that marks a struct type as a
// generated configuration: `//servo:config prefix=POSTGRES` in the type's
// doc comment. It is the one servo directive that lives in a comment
// rather than a spec-file marker call, because it annotates a type the
// generator must find wherever the type lives — a spec file cannot even
// name an unexported type in another package, and keeping the prefix on
// the struct keeps everything about one config in one place.
const ConfigDirective = "config"

// directivePrefix is what every servo comment directive starts with. The
// space of names after it is closed (only "config" today); servovet flags
// anything else so a typo'd directive fails in the editor instead of
// silently doing nothing.
const directivePrefix = "//servo:"

// ConfigTagKey is the struct tag key a //servo:config type's fields carry:
// `config:"name,required,default=10,secret"`.
const ConfigTagKey = "config"

// ConfigKind is the closed set of field types a //servo:config struct may
// declare. The set is exact basic types plus time.Duration — a defined
// type with a basic underlying (type Port int) is rejected rather than
// converted, so the generated loader never performs a conversion the
// struct's own package didn't write.
type ConfigKind int

const (
	KindString ConfigKind = iota
	KindBool
	KindInt
	KindInt8
	KindInt16
	KindInt32
	KindInt64
	KindUint
	KindUint8
	KindUint16
	KindUint32
	KindUint64
	KindFloat32
	KindFloat64
	KindDuration
)

// GoType is the type as the generated loader writes it.
func (k ConfigKind) GoType() string {
	switch k {
	case KindString:
		return "string"
	case KindBool:
		return "bool"
	case KindInt:
		return "int"
	case KindInt8:
		return "int8"
	case KindInt16:
		return "int16"
	case KindInt32:
		return "int32"
	case KindInt64:
		return "int64"
	case KindUint:
		return "uint"
	case KindUint8:
		return "uint8"
	case KindUint16:
		return "uint16"
	case KindUint32:
		return "uint32"
	case KindUint64:
		return "uint64"
	case KindFloat32:
		return "float32"
	case KindFloat64:
		return "float64"
	case KindDuration:
		return "time.Duration"
	}
	return "?"
}

// ConfigField is one parsed `config:"..."` field of a config struct.
type ConfigField struct {
	FieldName  string // the Go field name, as the loader assigns it
	Name       string // the tag name: lower_snake, the canonical setting name
	EnvName    string // PREFIX_NAME — the environment variable
	FileKey    string // the key within the type's file section (same as Name)
	Kind       ConfigKind
	Required   bool
	HasDefault bool
	Default    string // raw default text, validated against Kind at scan time
	// DefaultDuration is Default parsed, set only for KindDuration — the
	// emitter renders it as a readable duration expression rather than a
	// nanosecond count.
	DefaultDuration time.Duration
	Secret          bool
	Pos             token.Position
}

// ConfigDecl is one //servo:config type: the struct, its env prefix, its
// config-file section, and its parsed fields. The generated loader for it
// is a companion file in the type's own package, which is what lets the
// type and every one of its fields stay unexported.
type ConfigDecl struct {
	Key      Key
	Type     types.Type // the named struct type
	TypeName string     // e.g. "dbConfig"
	Prefix   string     // e.g. "POSTGRES" — env vars are PREFIX_NAME
	Section  string     // e.g. "postgres" — the config-file section key
	PkgPath  string
	PkgName  string
	Dir      string // directory the companion file is written into
	Pos      token.Position
	Fields   []ConfigField
}

var (
	prefixRe  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	sectionRe = nameRe
)

// ScanConfigs finds every //servo:config type declared in the main module
// and parses its directive and field tags, strictly: a malformed directive
// or tag is an error, not a silently ignored comment — the user wrote the
// directive, so refusing to half-honor it is the same judgement the spec
// parser applies to a malformed marker. Packages outside the main module
// are never scanned, because the companion file is written into the
// declaring package's directory and nothing should ever write into the
// module cache.
func ScanConfigs(pkgs []*packages.Package) ([]*ConfigDecl, error) {
	var decls []*ConfigDecl
	byPkg := map[string]*ConfigDecl{}
	for _, pkg := range pkgs {
		if pkg.Module == nil || !pkg.Module.Main || pkg.Types == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			found, err := configsInFile(pkg, file)
			if err != nil {
				return nil, err
			}
			for _, d := range found {
				// One config type per package keeps the generated loader's
				// exported name fixed (ServoConfig) instead of derived, and
				// matches how packages actually use configs — one struct of
				// settings per component package.
				if prior, dup := byPkg[d.PkgPath]; dup {
					return nil, fmt.Errorf("%s: second //servo:%s type in package %s — %s at %s already carries the directive, and a package's generated loader (ServoConfig) can only load one type; merge them or split the package", d.Pos, ConfigDirective, d.PkgPath, prior.TypeName, prior.Pos)
				}
				byPkg[d.PkgPath] = d
				decls = append(decls, d)
			}
		}
	}
	sort.Slice(decls, func(i, j int) bool { return ComparePos(decls[i].Pos, decls[j].Pos) < 0 })
	return decls, nil
}

// configsInFile parses every //servo:config directive in file. Directives
// are read from the type's doc comment — either the TypeSpec's own or, for
// the common single-type `type foo struct{...}` declaration, the GenDecl's.
func configsInFile(pkg *packages.Package, file *ast.File) ([]*ConfigDecl, error) {
	var out []*ConfigDecl
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
			line, pos, ok, err := configDirectiveIn(pkg.Fset, doc)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			d, err := buildConfigDecl(pkg, ts, line, pos)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// configDirectiveIn finds the one //servo:config line in doc. Unknown
// //servo: directives are an error here too, not only in servovet: the
// generator is the tool that must never silently skip one, since a typo'd
// directive means a config the author believes exists and doesn't.
func configDirectiveIn(fset *token.FileSet, doc *ast.CommentGroup) (line string, pos token.Position, found bool, err error) {
	if doc == nil {
		return "", token.Position{}, false, nil
	}
	for _, c := range doc.List {
		name, rest, ok := DirectiveLine(c.Text)
		if !ok {
			continue
		}
		p := fset.Position(c.Pos())
		if name != ConfigDirective {
			return "", token.Position{}, false, fmt.Errorf("%s: unrecognized servo directive //servo:%s — the only comment directive is //servo:%s", p, name, ConfigDirective)
		}
		if found {
			return "", token.Position{}, false, fmt.Errorf("%s: duplicate //servo:%s directive — first at %s", p, ConfigDirective, pos)
		}
		line, pos, found = rest, p, true
	}
	return line, pos, found, nil
}

// DirectiveLine splits a comment line of the form "//servo:name rest..."
// into its directive name and the text after it. Exported so servovet can
// recognize directives with the identical rule the generator uses.
func DirectiveLine(comment string) (name, rest string, ok bool) {
	if !strings.HasPrefix(comment, directivePrefix) {
		return "", "", false
	}
	body := strings.TrimPrefix(comment, directivePrefix)
	name, rest, _ = strings.Cut(body, " ")
	if name == "" {
		return "", "", false
	}
	return name, strings.TrimSpace(rest), true
}

// ParseConfigDirectiveOptions parses the option list after //servo:config:
// space-separated key=value pairs, of which prefix= is required and key= is
// the optional file-section override. Exported so servovet can validate a
// directive in the editor with the same rules the generator enforces.
func ParseConfigDirectiveOptions(rest string) (prefix, section string, err error) {
	for opt := range strings.FieldsSeq(rest) {
		k, v, ok := strings.Cut(opt, "=")
		if !ok {
			return "", "", fmt.Errorf("//servo:%s option %q is not key=value", ConfigDirective, opt)
		}
		switch k {
		case "prefix":
			if !prefixRe.MatchString(v) {
				return "", "", fmt.Errorf("//servo:%s prefix %q must be UPPER_SNAKE ([A-Z][A-Z0-9_]*)", ConfigDirective, v)
			}
			prefix = v
		case "key":
			if !sectionRe.MatchString(v) {
				return "", "", fmt.Errorf("//servo:%s key %q must be lower_snake ([a-z][a-z0-9_]*)", ConfigDirective, v)
			}
			section = v
		default:
			return "", "", fmt.Errorf("//servo:%s has no option %q — the options are prefix= (required) and key=", ConfigDirective, k)
		}
	}
	if prefix == "" {
		return "", "", fmt.Errorf("//servo:%s needs prefix=UPPER_SNAKE — it namespaces the environment variables (PREFIX_NAME) and, lowercased, names the config-file section", ConfigDirective)
	}
	if section == "" {
		section = strings.ToLower(prefix)
	}
	return prefix, section, nil
}

func buildConfigDecl(pkg *packages.Package, ts *ast.TypeSpec, rest string, dirPos token.Position) (*ConfigDecl, error) {
	prefix, section, err := ParseConfigDirectiveOptions(rest)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dirPos, err)
	}

	obj, ok := pkg.TypesInfo.Defs[ts.Name].(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("%s: //servo:%s type %s did not type-check", dirPos, ConfigDirective, ts.Name.Name)
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return nil, fmt.Errorf("%s: //servo:%s is on %s, which is not a struct — the directive generates a loader that fills struct fields from tags", dirPos, ConfigDirective, ts.Name.Name)
	}

	typePos := pkg.Fset.Position(ts.Pos())
	d := &ConfigDecl{
		Key:      NewKey(obj.Type(), ""),
		Type:     obj.Type(),
		TypeName: ts.Name.Name,
		Prefix:   prefix,
		Section:  section,
		PkgPath:  pkg.PkgPath,
		PkgName:  pkg.Types.Name(),
		Dir:      filepath.Dir(typePos.Filename),
		Pos:      typePos,
	}

	seen := map[string]token.Position{}
	for i := 0; i < st.NumFields(); i++ {
		fv := st.Field(i)
		tag, ok := reflect.StructTag(st.Tag(i)).Lookup(ConfigTagKey)
		if !ok {
			// An untagged field is simply not loaded — derived state a
			// constructor fills in later is an ordinary thing for a config
			// struct to carry.
			continue
		}
		fPos := pkg.Fset.Position(fv.Pos())
		if fv.Embedded() {
			return nil, fmt.Errorf("%s: //servo:%s field %s.%s: embedded fields cannot carry a config tag — nested config structs are not yet supported; declare the setting as a flat field", fPos, ConfigDirective, d.TypeName, fv.Name())
		}
		f, err := parseConfigField(fv, tag, prefix, fPos)
		if err != nil {
			return nil, err
		}
		if prior, dup := seen[f.Name]; dup {
			return nil, fmt.Errorf("%s: config name %q declared twice in %s — first at %s", fPos, f.Name, d.TypeName, prior)
		}
		seen[f.Name] = fPos
		d.Fields = append(d.Fields, f)
	}
	if len(d.Fields) == 0 {
		return nil, fmt.Errorf("%s: //servo:%s type %s has no `%s:\"...\"` tagged fields — nothing to load", dirPos, ConfigDirective, d.TypeName, ConfigTagKey)
	}
	return d, nil
}

// parseConfigField parses one `config:"name,opts..."` tag against the
// field's type. The grammar is deliberately four words: the name, required,
// default=<value>, secret. Anything smarter — ranges, cross-field rules —
// belongs in the constructor that receives the struct, which already
// returns an error at exactly the right moment in the lifecycle.
func parseConfigField(fv *types.Var, tag, prefix string, pos token.Position) (ConfigField, error) {
	parts := strings.Split(tag, ",")
	f := ConfigField{FieldName: fv.Name(), Name: parts[0], Pos: pos}
	if !nameRe.MatchString(f.Name) {
		return ConfigField{}, fmt.Errorf("%s: config tag on %s: name %q must be lower_snake ([a-z][a-z0-9_]*)", pos, fv.Name(), f.Name)
	}
	f.EnvName = prefix + "_" + strings.ToUpper(f.Name)
	f.FileKey = f.Name

	kind, ok := configKindOf(fv.Type())
	if !ok {
		return ConfigField{}, fmt.Errorf("%s: config field %s has unsupported type %s — supported types are string, bool, the sized ints/uints, float32/float64, and time.Duration (a nested struct is not yet supported)", pos, fv.Name(), fv.Type().String())
	}
	f.Kind = kind

	for _, opt := range parts[1:] {
		switch {
		case opt == "required":
			f.Required = true
		case opt == "secret":
			f.Secret = true
		case strings.HasPrefix(opt, "default="):
			f.HasDefault = true
			f.Default = strings.TrimPrefix(opt, "default=")
		case opt == "":
			return ConfigField{}, fmt.Errorf("%s: config tag on %s has an empty option (a stray comma?)", pos, fv.Name())
		default:
			return ConfigField{}, fmt.Errorf("%s: config tag on %s has unknown option %q — the options are required, default=<value>, and secret", pos, fv.Name(), opt)
		}
	}
	if f.Required && f.HasDefault {
		return ConfigField{}, fmt.Errorf("%s: config field %s is both required and has a default — a defaulted setting can never be missing, so pick one", pos, fv.Name())
	}
	if f.HasDefault {
		if err := validateDefault(&f); err != nil {
			return ConfigField{}, fmt.Errorf("%s: config field %s: default %q is not a valid %s: %v", pos, fv.Name(), f.Default, f.Kind.GoType(), err)
		}
	}
	return f, nil
}

// validateDefault checks the default text parses as the field's own type at
// generate time — the whole point of a generated loader is that a bad
// default is a `servo generate` error, never a startup one.
func validateDefault(f *ConfigField) error {
	var err error
	switch f.Kind {
	case KindString:
		// any text is a string
	case KindBool:
		_, err = strconv.ParseBool(f.Default)
	case KindInt, KindInt8, KindInt16, KindInt32, KindInt64:
		_, err = strconv.ParseInt(f.Default, 10, f.Kind.StrconvBits())
	case KindUint, KindUint8, KindUint16, KindUint32, KindUint64:
		_, err = strconv.ParseUint(f.Default, 10, f.Kind.StrconvBits())
	case KindFloat32, KindFloat64:
		_, err = strconv.ParseFloat(f.Default, f.Kind.StrconvBits())
	case KindDuration:
		f.DefaultDuration, err = time.ParseDuration(f.Default)
	}
	return err
}

// StrconvBits is the strconv bitSize for a sized kind; 0 for the
// platform-sized int/uint, exactly as strconv defines it. Float kinds
// return their own width for ParseFloat. A method beside GoType so each
// kind's facts live in one place, shared by default validation here and
// the emitted parse calls — which must agree, since the default is
// validated with the same bitSize the loader will parse with.
func (k ConfigKind) StrconvBits() int {
	switch k {
	case KindInt8, KindUint8:
		return 8
	case KindInt16, KindUint16:
		return 16
	case KindInt32, KindUint32, KindFloat32:
		return 32
	case KindInt64, KindUint64, KindFloat64:
		return 64
	default:
		return 0
	}
}

func configKindOf(t types.Type) (ConfigKind, bool) {
	u := types.Unalias(t)
	if named, ok := u.(*types.Named); ok {
		obj := named.Obj()
		if obj.Pkg() != nil && obj.Pkg().Path() == "time" && obj.Name() == "Duration" {
			return KindDuration, true
		}
		return 0, false
	}
	basic, ok := u.(*types.Basic)
	if !ok {
		return 0, false
	}
	switch basic.Kind() {
	case types.String:
		return KindString, true
	case types.Bool:
		return KindBool, true
	case types.Int:
		return KindInt, true
	case types.Int8:
		return KindInt8, true
	case types.Int16:
		return KindInt16, true
	case types.Int32:
		return KindInt32, true
	case types.Int64:
		return KindInt64, true
	case types.Uint:
		return KindUint, true
	case types.Uint8:
		return KindUint8, true
	case types.Uint16:
		return KindUint16, true
	case types.Uint32:
		return KindUint32, true
	case types.Uint64:
		return KindUint64, true
	case types.Float32:
		return KindFloat32, true
	case types.Float64:
		return KindFloat64, true
	default:
		return 0, false
	}
}
