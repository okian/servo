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
	// A package-level function, so allocated clear of the injector
	// package's own names like every other declaration the file adds.
	e.configFileFn = e.types.AllocateName(e.testPrefixed("servoLoadConfigFile"))
	// New's body calls the loader as a bare identifier in the same scope
	// as every node's local, so a node must not take the name either.
	e.names.AllocateName(e.configFileFn)
	e.configFileVar = e.names.AllocateName("configFile")
}

func (e *emitter) usesConfigFile() bool {
	return e.spec.ConfigFile != nil && len(e.resolved.Configs) > 0
}

// planConfigErrs names the local each config loader's error lands in,
// after every node has its own name. New runs every loader before it
// looks at any of their errors, so each needs a variable of its own; they
// are locals in New's flat scope, so they are reserved against user
// packages like the other locals New declares.
func (e *emitter) planConfigErrs() {
	for _, n := range e.resolved.Configs {
		name := e.names.AllocateName(e.varName[n.Key] + "Err")
		e.imports.Reserve(name)
		e.configErrVar[n] = name
	}
}

// configAssignments loads each used config at the very top of New — after
// the caller's supplied values, before anything is constructed. The value
// is deliberately a local, not an App field: the type is frequently
// unexported to its own package, and a field of it here would not compile.
// New's body is one flat scope, so every later construction can still
// take it by name.
//
// Every loader runs before any error is looked at, and their errors are
// joined, so one start names every missing setting in the app. New used to
// return at the first loader that failed, and a deploy missing four
// variables took four restarts to learn which; each loader already
// collects all of its own config's missing settings into one error. A
// config file that cannot be read stops everything at once, since every
// loader would be handed nothing.
func (e *emitter) configAssignments() string {
	if len(e.resolved.Configs) == 0 {
		return ""
	}
	loads, joined := e.loadConfigs(func(n *resolve.Node) string { return e.varName[n.Key] }, "nil, ")
	return loads + fmt.Sprintf("\tif err := %s; err != nil {\n\t\treturn nil, err\n\t}\n\n", joined)
}

// loadConfigs writes the config preamble New and ValidateConfig share —
// the file first, when one is declared, then every loader — and returns
// it with the expression that joins the loaders' errors. value names the
// local a loaded config lands in ("_" to discard it), and ret is what
// precedes the error when reading the file fails.
func (e *emitter) loadConfigs(value func(*resolve.Node) string, ret string) (loads, joined string) {
	var b strings.Builder
	if e.usesConfigFile() {
		fmt.Fprintf(&b, "\t%s, err := %s()\n", e.configFileVar, e.configFileFn)
		fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn %serr\n\t}\n\n", ret)
	}
	errs := make([]string, len(e.resolved.Configs))
	for i, n := range e.resolved.Configs {
		errs[i] = e.configErrVar[n]
		fmt.Fprintf(&b, "\t%s, %s := %s(%s)\n", value(n), errs[i], e.configLoaderRef(n), e.configLoaderArgs())
	}
	return b.String(), fmt.Sprintf("%s.Join(%s)", e.imports.Add("errors", "errors"), strings.Join(errs, ", "))
}

// validateConfigDecl emits ValidateConfig — only in the production
// variant, and only for a graph that loads a config. It runs exactly the
// loaders New runs, and reports exactly what New would, but constructs
// nothing: an init container or a deploy pipeline can learn that an
// environment is incomplete without starting the app, or reaching any of
// the things a constructor connects to.
func (e *emitter) validateConfigDecl() string {
	if !e.emitsValidateConfig() {
		return ""
	}
	var b strings.Builder
	b.WriteString("// ValidateConfig loads every //servo:config this graph uses, the config\n")
	b.WriteString("// file first when the spec declares one, and reports every missing or\n")
	b.WriteString("// malformed setting at once, as New would, without building anything.\n")
	b.WriteString("func ValidateConfig() error {\n")
	loads, joined := e.loadConfigs(func(*resolve.Node) string { return "_" }, "")
	fmt.Fprintf(&b, "%s\treturn %s\n}\n\n", loads, joined)
	return b.String()
}

// emitsValidateConfig reports whether this file declares ValidateConfig.
// The override variant does not: it lands in the same package as the
// production file, and a TestValidateConfig would be read as a malformed
// test.
func (e *emitter) emitsValidateConfig() bool {
	return !e.testMode && len(e.resolved.Configs) > 0
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
	dec := e.configDecoder(filepath.Ext(path))
	fp := e.imports.Add("path/filepath", "filepath")
	fmtPkg := e.imports.Add("fmt", "fmt")

	var b strings.Builder
	fmt.Fprintf(&b, "// %s reads the config file the spec declares (%s), or the\n", e.configFileFn, path)
	fmt.Fprintf(&b, "// path in %s. A missing file at the declared default path is fine —\n", ConfigFileEnvVar)
	b.WriteString("// every setting can still arrive from the environment — but a path set\n")
	b.WriteString("// explicitly must exist.\n")
	fmt.Fprintf(&b, "func %s() (map[string]any, error) {\n", e.configFileFn)
	fmt.Fprintf(&b, "\tpath, explicit := %q, false\n", path)
	fmt.Fprintf(&b, "\tif v, ok := os.LookupEnv(%q); ok {\n\t\tpath, explicit = v, true\n\t}\n", ConfigFileEnvVar)
	fmt.Fprintf(&b, "\tswitch %s.Ext(path) {\n\tcase %s:\n\tdefault:\n", fp, quotedList(dec.exts))
	fmt.Fprintf(&b, "\t\treturn nil, %s.Errorf(\"config file %%s: unsupported extension — this binary was generated for %s\", path)\n\t}\n", fmtPkg, strings.Join(dec.exts, "/"))
	b.WriteString("\tdata, err := os.ReadFile(path)\n")
	b.WriteString("\tif err != nil {\n")
	b.WriteString("\t\tif os.IsNotExist(err) && !explicit {\n\t\t\treturn nil, nil\n\t\t}\n")
	fmt.Fprintf(&b, "\t\treturn nil, %s.Errorf(\"config file %%s: %%w\", path, err)\n", fmtPkg)
	b.WriteString("\t}\n")
	b.WriteString("\tvar file map[string]any\n")
	fmt.Fprintf(&b, "\tif err := %s(data, &file); err != nil {\n", dec.call)
	e.writeDecodeError(&b, dec, fmtPkg)
	b.WriteString("\t}\n")
	b.WriteString("\treturn file, nil\n}\n\n")
	return b.String()
}

// configDecoderPlan is what the file-reading helper needs to know about
// one format: the runtime extensions it accepts, the unmarshal call, and
// how to report a file that does not decode.
type configDecoderPlan struct {
	exts   []string
	call   string
	format string // JSON, YAML, TOML
	// linePrefix is the prefix the decoder's message opens with, followed
	// by the line number; "" for JSON, whose syntax error carries a byte
	// offset instead.
	linePrefix string
}

// configDecoder maps the declared extension to its decoder, registering
// the decoder's import. load already validated the extension, so the
// default arm is a servo bug, not a user error.
func (e *emitter) configDecoder(ext string) configDecoderPlan {
	switch ext {
	case ".json":
		return configDecoderPlan{exts: []string{".json"}, call: e.imports.Add("encoding/json", "json") + ".Unmarshal", format: "JSON"}
	case ".yaml", ".yml":
		return configDecoderPlan{exts: []string{".yaml", ".yml"}, call: e.imports.Add("gopkg.in/yaml.v3", "yaml") + ".Unmarshal", format: "YAML", linePrefix: "yaml: line"}
	case ".toml":
		return configDecoderPlan{exts: []string{".toml"}, call: e.imports.Add("github.com/BurntSushi/toml", "toml") + ".Unmarshal", format: "TOML", linePrefix: "toml: line"}
	default:
		panic(fmt.Sprintf("emit: unvalidated config file extension %q reached the emitter (this is a servo bug)", ext))
	}
}

// writeDecodeError reports a file that does not decode by its path and,
// where the decoder says, its line — never by the decoder's own message.
// Those quote the file: TOML's for an unquoted `password =
// correcthorsebatterystaple` is `expected value but found
// "correcthorsebatterystaple" instead`, and for an out-of-range number it
// prints the number. The error is reported before any section is read, so
// whether the offending line belongs to a secret field cannot be known
// here, and the message is withheld for every file. The line is read from
// the message's stable prefix for YAML and TOML rather than from either
// library's error type, which differs between their releases, and from the
// syntax error's byte offset for JSON.
func (e *emitter) writeDecodeError(b *strings.Builder, dec configDecoderPlan, fmtPkg string) {
	why := fmt.Sprintf("not valid %s (the decoder's message is not shown: it can quote the file, secrets included)", dec.format)
	if dec.linePrefix == "" {
		jsonPkg := e.imports.Add("encoding/json", "json")
		errorsPkg := e.imports.Add("errors", "errors")
		bytesPkg := e.imports.Add("bytes", "bytes")
		fmt.Fprintf(b, "\t\tvar syntax *%s.SyntaxError\n", jsonPkg)
		fmt.Fprintf(b, "\t\tif %s.As(err, &syntax) {\n", errorsPkg)
		fmt.Fprintf(b, "\t\t\treturn nil, %s.Errorf(\"config file %%s: line %%d: %s\", path, 1+%s.Count(data[:syntax.Offset], []byte(\"\\n\")))\n\t\t}\n", fmtPkg, why, bytesPkg)
	} else {
		b.WriteString("\t\tvar line int\n")
		fmt.Fprintf(b, "\t\tif _, scanErr := %s.Sscanf(err.Error(), \"%s %%d\", &line); scanErr == nil {\n", fmtPkg, dec.linePrefix)
		fmt.Fprintf(b, "\t\t\treturn nil, %s.Errorf(\"config file %%s: line %%d: %s\", path, line)\n\t\t}\n", fmtPkg, why)
	}
	fmt.Fprintf(b, "\t\treturn nil, %s.Errorf(\"config file %%s: %s\", path)\n", fmtPkg, why)
}

func quotedList(ss []string) string {
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, ", ")
}
