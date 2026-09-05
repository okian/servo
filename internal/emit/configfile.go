package emit

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/okian/servo/v3/internal/resolve"
)

// ConfigFileEnvVar is the runtime override for the declared config file
// path. Fixed rather than derived, so an operator can learn it once.
const ConfigFileEnvVar = "CONFIG_FILE"

// planConfigs claims the identifiers config emission hard-codes — only
// when the graph actually uses a config, so every other graph's output
// stays byte-identical.
func (e *emitter) planConfigs() {
	if !e.usesConfigFile() {
		return
	}
	e.configFileFn = e.testPrefixed("servoLoadConfigFile")
	e.types.AllocateName(e.configFileFn)
	// New's body calls the loader as a bare identifier in the same scope
	// as every node's local, so a node must not take the name either.
	e.names.AllocateName(e.configFileFn)
	e.configFileVar = e.names.AllocateName("configFile")
}

func (e *emitter) usesConfigFile() bool {
	return e.spec.ConfigFile != nil && len(e.resolved.Configs) > 0
}

// configAssignments loads each used config at the very top of New — after
// the caller's supplied values, before anything is constructed. The value
// is deliberately a local, not an App field: the type is frequently
// unexported to its own package, and a field of it here would not compile.
// New's body is one flat scope, so every later construction can still
// take it by name.
func (e *emitter) configAssignments() string {
	var b strings.Builder
	if e.usesConfigFile() {
		fmt.Fprintf(&b, "\t%s, err := %s()\n", e.configFileVar, e.configFileFn)
		b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n\n")
	}
	for _, n := range e.resolved.Configs {
		fmt.Fprintf(&b, "\t%s, err := %s(%s)\n", e.varName[n.Key], e.configLoaderRef(n), e.configLoaderArgs())
		b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n\n")
	}
	return b.String()
}

// configLoaderRef renders the generated loader's call target: qualified
// from any other package, bare when the config lives in the injector's own
// (importing your own package is a compile error).
func (e *emitter) configLoaderRef(n *resolve.Node) string {
	if n.Config.PkgPath == e.spec.InjectorPkg.PkgPath {
		return ConfigLoaderFuncName
	}
	return e.imports.Add(n.Config.PkgPath, n.Config.PkgName) + "." + ConfigLoaderFuncName
}

func (e *emitter) configLoaderArgs() string {
	if e.spec.ConfigFile != nil {
		return e.configFileVar
	}
	return ""
}

// configFileDecl emits the file-reading helper the preamble calls, or
// nothing for an env-only injector. The decoder is chosen at generate time
// from the declared path's extension — the whole point of declaring the
// path as a literal — so an env-only or json-only binary never links a
// yaml parser.
func (e *emitter) configFileDecl() string {
	if !e.usesConfigFile() {
		return ""
	}
	path := e.spec.ConfigFile.Path
	exts, decodeCall := e.configDecoder(filepath.Ext(path))
	fp := e.imports.Add("path/filepath", "filepath")
	e.imports.Add("fmt", "fmt")

	var b strings.Builder
	fmt.Fprintf(&b, "// %s reads the config file the spec declares (%s), or the\n", e.configFileFn, path)
	fmt.Fprintf(&b, "// path in %s. A missing file at the declared default path is fine —\n", ConfigFileEnvVar)
	b.WriteString("// every setting can still arrive from the environment — but a path set\n")
	b.WriteString("// explicitly must exist.\n")
	fmt.Fprintf(&b, "func %s() (map[string]any, error) {\n", e.configFileFn)
	fmt.Fprintf(&b, "\tpath, explicit := %q, false\n", path)
	fmt.Fprintf(&b, "\tif v, ok := os.LookupEnv(%q); ok {\n\t\tpath, explicit = v, true\n\t}\n", ConfigFileEnvVar)
	fmt.Fprintf(&b, "\tswitch %s.Ext(path) {\n\tcase %s:\n\tdefault:\n", fp, quotedList(exts))
	fmt.Fprintf(&b, "\t\treturn nil, fmt.Errorf(\"config file %%s: unsupported extension — this binary was generated for %s\", path)\n\t}\n", strings.Join(exts, "/"))
	b.WriteString("\tdata, err := os.ReadFile(path)\n")
	b.WriteString("\tif err != nil {\n")
	b.WriteString("\t\tif os.IsNotExist(err) && !explicit {\n\t\t\treturn nil, nil\n\t\t}\n")
	b.WriteString("\t\treturn nil, fmt.Errorf(\"config file %s: %w\", path, err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tvar file map[string]any\n")
	fmt.Fprintf(&b, "\tif err := %s(data, &file); err != nil {\n", decodeCall)
	b.WriteString("\t\treturn nil, fmt.Errorf(\"config file %s: %w\", path, err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn file, nil\n}\n\n")
	return b.String()
}

// configDecoder maps the declared extension to the runtime extensions it
// accepts and the unmarshal call to emit, registering the decoder's import.
// load already validated the extension, so the default arm is a servo bug,
// not a user error.
func (e *emitter) configDecoder(ext string) (exts []string, decodeCall string) {
	switch ext {
	case ".json":
		return []string{".json"}, e.imports.Add("encoding/json", "json") + ".Unmarshal"
	case ".yaml", ".yml":
		return []string{".yaml", ".yml"}, e.imports.Add("gopkg.in/yaml.v3", "yaml") + ".Unmarshal"
	case ".toml":
		return []string{".toml"}, e.imports.Add("github.com/BurntSushi/toml", "toml") + ".Unmarshal"
	default:
		panic(fmt.Sprintf("emit: unvalidated config file extension %q reached the emitter (this is a servo bug)", ext))
	}
}

func quotedList(ss []string) string {
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, ", ")
}
