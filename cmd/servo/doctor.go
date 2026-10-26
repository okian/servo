package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

// servoToolPath is the generator's package path: what `go get -tool`
// records in go.mod, and what `go tool` accepts in place of the short
// name.
var servoToolPath = path.Dir(graph.ServoPackagePath) + "/cmd/servo"

// runDoctor diagnoses setup problems before `go generate` is ever run,
// across every injector found under dir — the same scope generate/check
// process, not just one: can the spec(s) be found at all (which, via
// FindSpecs, already enforces the build-tag requirement), is this servo
// the version the module builds against, will `go generate` reach the
// generator at all — a directive go generate can see, covering each
// injector, naming a tool go.mod declares — is the module otherwise free
// of build errors outside the injector(s), do the injectors agree about
// shared config loaders, is a config loader left over from a type that
// is gone, does each generated file exist, is it fresh, does what is
// fresh compile, and — best-effort, never fatal — is it tracked by VCS.
func runDoctor(cfg load.Config) error {
	return runDoctorWith(cfg, invocation{})
}

// runDoctorWith is runDoctor with the loading flags the run was given,
// which every command it suggests repeats: from anywhere but the
// directory doctor ran in, "run `servo generate`" without the --dir is
// wrong advice.
func runDoctorWith(cfg load.Config, invoked invocation) error {
	fmt.Println("servo doctor:")
	problems := false
	report := func(ok bool, format string, args ...any) {
		status := "OK  "
		if !ok {
			status = "FAIL"
			problems = true
		}
		fmt.Printf("  [%s] %s\n", status, fmt.Sprintf(format, args...))
	}
	note := func(status, format string, args ...any) {
		fmt.Printf("  [%s] %s\n", status, fmt.Sprintf(format, args...))
	}

	loaded, caps, routes, configs, err := loadModule(cfg)
	if err != nil {
		report(false, "load module: %v", err)
		return fmt.Errorf("servo doctor: problems found")
	}
	specs, err := load.FindSpecs(loaded)
	if err != nil {
		report(false, "find spec file(s): %v", err)
		return fmt.Errorf("servo doctor: problems found")
	}

	// First, because every freshness verdict below is a comparison with
	// what this binary would generate, and that is only the module's
	// answer when this binary is the servo the module builds against.
	skew := versionSkew(releaseVersion(), loaded.ServoPkg.Module)
	switch skew.status {
	case "OK":
		report(true, "%s", skew.msg)
	case "WARN", "INFO":
		note(skew.status, "%s", skew.msg)
	}

	if gowork, uses, err := workspaceModules(cfg.Dir); err == nil && len(uses) > 1 {
		note("WARN", "%s lists %d modules (%s). Each injector resolves against its own module — the default --scope=module — so another listed module's types are never auto-bound and no generated file is written into it; --scope=workspace treats every listed module as part of the graph. CI usually builds without the go.work (GOWORK=off), so generate the way CI will", gowork, len(uses), strings.Join(uses, ", "))
	}

	injectorPaths := make([]string, len(specs))
	for i, s := range specs {
		injectorPaths[i] = s.InjectorPkg.PkgPath
	}
	if err := loaded.NonInjectorErrors(injectorPaths...); err != nil {
		report(false, "module has build errors outside the injector(s): %v", err)
	} else {
		report(true, "no build errors outside the injector(s)")
	}

	// An injector whose spec is gated out of this configuration gets no
	// generated file here, so its main.go's call to New has nothing behind
	// it. Nothing else reports this: generate and check are right to stay
	// silent (an injector deliberately excluded from a configuration is
	// legitimate), but the compiler's eventual "undefined: New" explains
	// none of it.
	for _, pkgPath := range loaded.InjectorsInOtherConfigurations(injectorPaths...) {
		report(false, "%s holds a spec file this configuration cannot see, so nothing generates its New — either give it a variant for these flags, or gate the package itself out of this build", pkgPath)
	}

	// The route checks are about the module, so they read every injector
	// in it; --dir only chose which ones this run diagnoses. FindSpecs
	// already parsed them all, so this cannot fail where it did not.
	moduleSpecs, _ := load.ModuleSpecs(loaded)
	if err := checkRouteGroups(moduleSpecs, routes); err != nil {
		report(false, "route groups: %v", err)
	}
	if err := checkMainPackageRoutes(moduleSpecs, routes); err != nil {
		report(false, "routes in package main: %v", err)
	}

	// Routes with no server are not an error — a variant spec may declare
	// servo.HTTP() under flags this run wasn't given — but silently unserved
	// endpoints are exactly the kind of gap only doctor is positioned to
	// name.
	if len(routes) > 0 {
		served := false
		for _, s := range moduleSpecs {
			if s.HTTP != nil {
				served = true
				break
			}
		}
		if !served {
			note("INFO", "%d //servo: route(s) found, but no injector in this configuration declares servo.HTTP(), so nothing serves them", len(routes))
		}
	}

	// The go:generate lines, read once per module the injectors live in.
	// Whether go generate reaches the generator is a property of the
	// module, but the verdict is per injector, below.
	directives := map[string]*moduleDirectives{}
	for _, spec := range specs {
		mod := spec.InjectorPkg.Module
		if mod == nil || mod.Dir == "" || directives[mod.Dir] != nil {
			continue
		}
		md, err := scanGenerateDirectives(mod.Dir, loaded.Tags)
		if err != nil {
			note("WARN", "could not read the module's go:generate lines: %v", err)
			continue
		}
		directives[mod.Dir] = md
		reportToolDirective(md, cfg, mod, report)
	}

	// Resolved together, through the call generate and check make, so the
	// module-wide check among them runs here too: injectors sharing a
	// //servo:config must agree on servo.ConfigFile, since its loader is
	// one file with one signature. Without it doctor passed a module that
	// generate then refused.
	pipelines := make([]*pipeline, len(specs))
	for i, spec := range specs {
		pipelines[i] = pipelineFor(loaded, caps, routes, configs, spec)
	}
	resolveds, _, agreementErr := resolveAll(pipelines)
	if agreementErr != nil {
		report(false, "config loaders: %v", agreementErr)
	}
	// A companion loader whose //servo:config type is gone still compiles
	// into its package, and nothing regenerates it — the same search check
	// makes, one FAIL per file.
	orphans, err := orphanedCompanions(pipelines)
	if err != nil {
		report(false, "looking for leftover config loaders: %v", err)
	}
	for _, path := range orphans {
		report(false, "%s is left over from a //servo:config type its package no longer declares — run %s, which removes it", path, invoked.command("generate", loaded.Tags))
	}
	skewNote := ""
	if skew.status == "WARN" {
		skewNote = " — though this servo is not the version go.mod requires; see the WARN above"
	}

	// One checker for every injector, as check uses one: it keeps the files
	// found current, so they are compiled together once the loop is done.
	c := newChecker(invoked)
	multi := len(specs) > 1
	for i, spec := range specs {
		if multi {
			fmt.Printf("  -- %s --\n", spec.InjectorPkg.PkgPath)
		}
		report(true, "spec file found at %s, correctly gated by the servoinject build tag", spec.Pos)
		if mod := spec.InjectorPkg.Module; mod != nil && directives[mod.Dir] != nil {
			reportGenerateCoverage(directives[mod.Dir], spec, report, note)
		}

		outPath := filepath.Join(filepath.Dir(spec.Pos.Filename), variantFileName(spec.Variant, false))
		if _, statErr := os.Stat(outPath); statErr != nil {
			report(false, "generated file missing: %s (run %s)", outPath, invoked.command("generate", spec.Variant))
			continue
		}
		report(true, "generated file present: %s", outPath)

		pl := pipelines[i]
		if resolveds[i] == nil {
			// resolveAll keeps its failures as one list; resolving this
			// injector again is the simplest way to put its own under its
			// own heading. It used to be reported as a stale file, which
			// sent people to regenerate a graph that could not resolve.
			_, err := pl.resolve(nil)
			report(false, "the graph does not resolve, so nothing can be generated from it: %v", err)
		} else if err := c.checkOne(pl, resolveds[i]); err != nil {
			report(false, "generated file is stale (run %s)%s: %v", invoked.command("generate", spec.Variant), skewNote, err)
		} else {
			report(true, "generated file matches a fresh generation")
		}
		// The override variant is checked the same way, and a leftover one
		// — written when the spec still declared an Override — is a FAIL:
		// nothing regenerates it, so it drifts from the moment it is
		// orphaned, like a tagged variant whose spec was deleted.
		if err := c.checkTestVariant(pl); err != nil {
			report(false, "override variant: %v", err)
		} else if len(spec.Overrides) > 0 {
			report(true, "override variant present: %s, matches a fresh generation", filepath.Join(filepath.Dir(spec.Pos.Filename), variantFileName(spec.Variant, true)))
		}
		if trackedByGit(cfg.Dir, outPath) {
			report(true, "generated file is tracked by git")
		} else {
			note("WARN", "generated file may not be committed — it should be, so builds are reproducible without running `servo generate` first")
		}

		// Everything above concerns the one variant these flags select.
		// Without the inventory below, a project's other variants are
		// invisible to every servo command: a stale prod variant, or one
		// whose spec was deleted while its generated file stayed behind,
		// draws three green servo commands next to a red `go build`.
		if err := reportVariants(spec, variantFileName(spec.Variant, false), invoked, report); err != nil {
			report(false, "reading sibling generated files: %v", err)
		}
	}

	// Matching a fresh generation byte for byte is not compiling: a Bind to
	// a mock whose interface has since grown a method generates the same
	// broken file every time, and doctor called it fresh while go build
	// failed inside it. The files found current are compiled once,
	// together, as check compiles them.
	if err := c.verify(cfg); err != nil {
		report(false, "%v", err)
	} else if len(c.current) > 0 {
		report(true, "the generated files that match a fresh generation compile")
	}

	if problems {
		return fmt.Errorf("servo doctor: problems found")
	}
	return nil
}

// skewVerdict is doctor's version line: a status and what to say.
type skewVerdict struct{ status, msg string }

// versionSkew compares this binary's release with the servo the module
// builds against. Two servo versions disagree about what a fresh
// generation is, so every stale verdict that follows is only meaningful
// when they match — and the README's own setup, a global `@latest` beside
// a module pinned by `go get -tool`, makes a mismatch the normal state
// for anyone not running `go tool servo`.
func versionSkew(binary string, servoMod *packages.Module) skewVerdict {
	switch {
	case servoMod == nil:
		return skewVerdict{}
	case servoMod.Main:
		return skewVerdict{"INFO", "this is servo's own module, so the generator and the runtime are the same checkout"}
	case servoMod.Replace != nil:
		target := servoMod.Replace.Path
		if servoMod.Replace.Version != "" {
			target += " " + servoMod.Replace.Version
		}
		return skewVerdict{"INFO", fmt.Sprintf("go.mod replaces %s with %s, so this servo (%s) has no release to be compared against", servoMod.Path, target, version())}
	case binary == "":
		return skewVerdict{"INFO", fmt.Sprintf("this servo is a development build, %s, so it cannot be compared with the %s go.mod requires", version(), servoMod.Version)}
	case binary == servoMod.Version:
		return skewVerdict{"OK", fmt.Sprintf("servo %s, the version go.mod requires", binary)}
	}
	return skewVerdict{"WARN", fmt.Sprintf("this servo is %s, but go.mod requires %s %s — a generated file is only fresh relative to the generator that wrote it, so run the module's own: `go tool servo` (after `go get -tool %s`). Files generated since v3.5.0 also carry a compile-time contract guard (servoimpl.Contract1), so code from a generator newer than the runtime stops at an undefined servoimpl constant instead of misbehaving at run time",
		binary, servoMod.Path, servoMod.Version, servoToolPath)}
}

// workspaceModules is the go.work a run from dir is under, and the
// directories its use directives list — nothing when there is none,
// GOWORK=off included. Asked of the go command, so GOWORK and the search
// upward from dir behave exactly as they do for go build.
func workspaceModules(dir string) (gowork string, uses []string, err error) {
	env := exec.Command("go", "env", "GOWORK")
	env.Dir = dir
	out, err := env.Output()
	if err != nil {
		return "", nil, err
	}
	gowork = strings.TrimSpace(string(out))
	if gowork == "" || gowork == "off" {
		return "", nil, nil
	}
	edit := exec.Command("go", "work", "edit", "-json", gowork)
	edit.Dir = dir
	out, err = edit.Output()
	if err != nil {
		return "", nil, err
	}
	var work struct{ Use []struct{ DiskPath string } }
	if err := json.Unmarshal(out, &work); err != nil {
		return "", nil, err
	}
	for _, u := range work.Use {
		uses = append(uses, u.DiskPath)
	}
	return gowork, uses, nil
}

// generateDirective is one //go:generate line that runs servo's generate
// command, as go generate would run it.
type generateDirective struct {
	file string
	line int
	// text is the directive as written, for messages.
	text string
	// live reports whether go generate sees the file at all: it honours
	// build constraints, so a directive inside the servoinject-gated spec
	// file never runs.
	live bool
	// root is the directory the run scans, its --dir resolved against the
	// file's own directory, which is where go generate runs it.
	root string
	// tags is the run's canonical --tags: the variant it generates.
	tags []string
	// goTool marks `go tool servo …`, which needs go.mod's tool directive.
	goTool bool
	// err is set when servo would refuse the line's arguments.
	err error
}

func (d generateDirective) pos() string { return d.file + ":" + strconv.Itoa(d.line) }

// covers reports whether d's run generates the injector in dir: servo
// generate processes every injector under its --dir.
func (d generateDirective) covers(dir string) bool {
	rel, err := filepath.Rel(d.root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// moduleDirectives is every servo-generating go:generate line in one
// module, live or not, for every variant: the per-injector verdicts need
// to know whether the module uses go generate for servo anywhere.
type moduleDirectives struct {
	servo []generateDirective
}

// scanGenerateDirectives reads every .go file in the module rooted at
// moduleDir the way go generate does — line by line, a directive being a
// line that starts with "//go:generate", words split and quoted the same
// way, -command shorthands honoured — and keeps the ones that run servo
// generate. Nested modules, testdata, vendor and the directories the go
// command ignores are skipped, as `./...` skips them. tags is this run's
// tag set: go generate evaluates build constraints with the generate tag
// added, and doctor checks the variant those tags select.
func scanGenerateDirectives(moduleDir string, tags []string) (*moduleDirectives, error) {
	ctxt := build.Default
	ctxt.BuildTags = append(append([]string(nil), tags...), "generate")
	md := &moduleDirectives{}
	err := filepath.WalkDir(moduleDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p == moduleDir {
				return nil
			}
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Contains(src, []byte("//go:generate")) {
			return nil
		}
		live, _ := ctxt.MatchFile(filepath.Dir(p), name)
		md.servo = append(md.servo, servoDirectivesIn(p, src, live)...)
		return nil
	})
	return md, err
}

// servoDirectivesIn is the servo generate directives in one file's source.
func servoDirectivesIn(file string, src []byte, live bool) []generateDirective {
	var out []generateDirective
	shorthands := map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if !strings.HasPrefix(text, "//go:generate ") && !strings.HasPrefix(text, "//go:generate\t") {
			continue
		}
		words := splitGenerateLine(strings.TrimRight(text[len("//go:generate "):], "\r"), file, line)
		if len(words) == 0 {
			continue
		}
		if words[0] == "-command" {
			if len(words) > 2 {
				shorthands[words[1]] = words[2:]
			}
			continue
		}
		if expansion, ok := shorthands[words[0]]; ok {
			words = append(append([]string(nil), expansion...), words[1:]...)
		}
		args, goTool, ok := servoInvocation(words)
		if !ok {
			continue
		}
		root, runTags, isGenerate, err := generateRunOf(args)
		if !isGenerate {
			continue
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(filepath.Dir(file), root)
		}
		out = append(out, generateDirective{
			file: file, line: line, text: strings.TrimSpace(text), live: live,
			root: filepath.Clean(root), tags: runTags, goTool: goTool, err: err,
		})
	}
	return out
}

// splitGenerateLine splits a directive's text into words the way go
// generate does: on spaces and tabs, a double-quoted word unquoted, $NAME
// expanded from the variables go generate sets and then the environment.
// A malformed quote ends the line, as it ends go generate's run.
func splitGenerateLine(line, file string, lineNum int) []string {
	vars := map[string]string{
		"GOFILE":    filepath.Base(file),
		"GOLINE":    strconv.Itoa(lineNum),
		"GOPACKAGE": "",
		"GOOS":      build.Default.GOOS,
		"GOARCH":    build.Default.GOARCH,
		"GOROOT":    build.Default.GOROOT,
		"DOLLAR":    "$",
	}
	expand := func(s string) string {
		return os.Expand(s, func(name string) string {
			if v, ok := vars[name]; ok {
				return v
			}
			return os.Getenv(name)
		})
	}
	var words []string
	for {
		line = strings.TrimLeft(line, " \t")
		if line == "" {
			return words
		}
		if line[0] == '"' {
			end := 1
			for end < len(line) && line[end] != '"' {
				if line[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(line) {
				return words
			}
			word, err := strconv.Unquote(line[:end+1])
			if err != nil {
				return words
			}
			words = append(words, expand(word))
			line = line[end+1:]
			continue
		}
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			i = len(line)
		}
		words = append(words, expand(line[:i]))
		line = line[i:]
	}
}

// goRunValueFlags are the go run flags that take a separate value, so
// the scan past them to the package being run skips the value too.
var goRunValueFlags = map[string]bool{
	"C": true, "p": true, "asmflags": true, "buildmode": true, "compiler": true, "gccgoflags": true,
	"gcflags": true, "installsuffix": true, "ldflags": true, "mod": true, "modfile": true, "overlay": true,
	"pgo": true, "pkgdir": true, "tags": true, "toolexec": true, "exec": true, "coverpkg": true, "covermode": true,
}

// servoInvocation recognises a directive that runs the servo CLI —
// `go tool servo`, `go tool <its package path>`, `go run <its package>`
// (a module path or a relative ./cmd/servo), or a servo binary named
// directly — and returns the arguments servo itself receives.
func servoInvocation(words []string) (args []string, goTool, ok bool) {
	if len(words) == 0 {
		return nil, false, false
	}
	if words[0] == "go" && len(words) > 1 {
		i := 2
		switch words[1] {
		case "tool":
			for i < len(words) && strings.HasPrefix(words[i], "-") {
				i++
			}
			if i < len(words) && (words[i] == "servo" || words[i] == servoToolPath) {
				return words[i+1:], true, true
			}
		case "run":
			for i < len(words) && strings.HasPrefix(words[i], "-") {
				flagName := strings.TrimLeft(words[i], "-")
				i++
				if !strings.Contains(flagName, "=") && goRunValueFlags[flagName] {
					i++
				}
			}
			if i < len(words) {
				pkg, _, _ := strings.Cut(words[i], "@")
				pkg = filepath.ToSlash(pkg)
				if pkg == servoToolPath || strings.HasSuffix(pkg, "/cmd/servo") {
					return words[i+1:], false, true
				}
			}
		}
		return nil, false, false
	}
	if filepath.Base(words[0]) == "servo" {
		return words[1:], false, true
	}
	return nil, false, false
}

// generateRunOf reads a servo invocation's arguments as servo would: is
// it a generation — `generate`, or no command at all, the default — and
// if so, the --dir it scans and the --tags it generates under. The flags
// are parsed by generate's own flag set, so a flag generate gains is
// understood here without a second list.
func generateRunOf(args []string) (dir string, tags []string, isGenerate bool, err error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if args[0] != "generate" {
			return "", nil, false, nil
		}
		args = args[1:]
	}
	fs := newFlagSet("generate")
	setupGenerate(fs)
	if _, err := parseFlags(fs, args); err != nil {
		return ".", nil, true, err
	}
	return fs.Lookup("dir").Value.String(), load.BuildFlags{Tags: fs.Lookup("tags").Value.String()}.TagList(), true, nil
}

// reportToolDirective checks the half of `go tool servo` that lives in
// go.mod. Without a tool directive the go command does not know the name,
// and a scaffolded `go generate` fails with `no such tool "servo"` — the
// step every README reader can skip, since nothing else needs it.
func reportToolDirective(md *moduleDirectives, cfg load.Config, mod *packages.Module, report func(bool, string, ...any)) {
	var viaTool *generateDirective
	for i, d := range md.servo {
		if d.goTool && d.live {
			viaTool = &md.servo[i]
			break
		}
	}
	if viaTool == nil {
		return
	}
	gomod := mod.GoMod
	if cfg.Build.ModFile != "" {
		gomod = cfg.Build.ModFile
	}
	edit := exec.Command("go", "mod", "edit", "-json", gomod)
	edit.Dir = mod.Dir
	out, err := edit.Output()
	if err != nil {
		report(false, "reading %s for its tool directives: %v", gomod, err)
		return
	}
	var file struct{ Tool []struct{ Path string } }
	if err := json.Unmarshal(out, &file); err != nil {
		report(false, "reading %s for its tool directives: %v", gomod, err)
		return
	}
	for _, t := range file.Tool {
		if t.Path == servoToolPath {
			report(true, "%s declares the servo tool that `go tool servo` runs", gomod)
			return
		}
	}
	report(false, "%s runs `go tool servo`, but %s has no tool directive for it, so go generate fails with `no such tool \"servo\"` — run `go get -tool %s`", viaTool.pos(), gomod, servoToolPath)
}

// reportGenerateCoverage says whether `go generate ./...` regenerates
// spec's injector: a directive go generate can see, whose run covers the
// injector's directory under the tags this run checks. A directive inside
// the servoinject-gated spec file is the silent case it exists for —
// go generate honours build constraints, exits 0, and generates nothing.
//
// A module with no servo directive anywhere is a WARN rather than a FAIL:
// regenerating from a Makefile or a CI script is a legitimate setup, and
// a doctor that can never go green for it would be switched off. Once a
// module does use go generate for servo, an injector none of its lines
// covers is a FAIL, as is a covering line servo would refuse.
func reportGenerateCoverage(md *moduleDirectives, spec *load.Spec, report func(bool, string, ...any), note func(string, string, ...any)) {
	injectorDir := filepath.Dir(spec.Pos.Filename)
	want := regenerateCommand(spec.Variant)
	var live, dead, broken []generateDirective
	for _, d := range md.servo {
		if !d.covers(injectorDir) || !slices.Equal(d.tags, spec.Variant) {
			continue
		}
		switch {
		case d.err != nil:
			broken = append(broken, d)
		case d.live:
			live = append(live, d)
		default:
			dead = append(dead, d)
		}
	}
	for _, d := range broken {
		report(false, "%s will fail when go generate runs it — %v: %s", d.pos(), d.err, d.text)
	}
	switch {
	case len(live) > 0:
		report(true, "go generate runs %s for this injector: %s", want, live[0].pos())
	case len(dead) > 0:
		report(false, "go generate never runs %s for this injector: %s is in a file go generate skips, because its build constraint (%s) is false there — go generate honours build constraints. Move the line to an untagged file; servo init writes one, %s",
			want, dead[0].pos(), buildConstraintOf(dead[0].file), generateFileName)
	case len(broken) > 0:
		// Already reported above.
	case len(md.servo) > 0:
		report(false, "no //go:generate line runs %s over this injector, though the module's others run servo — so go generate ./... leaves its generated file stale. Add `//go:generate go tool servo generate%s` to an untagged file in %s (servo init writes %s), or widen an existing line's --dir",
			want, tagsFlag(spec.Variant), injectorDir, generateFileName)
	default:
		note("WARN", "no //go:generate line in this module runs servo, so go generate ./... never regenerates this injector — fine if make or CI runs %s instead; otherwise add `//go:generate go tool servo generate%s` to an untagged file in %s (servo init writes %s)",
			want, tagsFlag(spec.Variant), injectorDir, generateFileName)
	}
}

// tagsFlag is the --tags a directive needs to generate the variant tags
// select, empty for the default one.
func tagsFlag(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return " --tags=" + strings.Join(tags, ",")
}

// buildConstraintOf is the //go:build line a file carries, for a message
// about why go generate skipped it.
func buildConstraintOf(file string) string {
	src, err := os.ReadFile(file)
	if err != nil {
		return "its build constraint"
	}
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "//go:build ") {
			return "`" + line + "`"
		}
		if strings.HasPrefix(line, "package ") {
			break
		}
	}
	return "its file name or build tags"
}

// reportVariants inventories the other generated files sitting beside this
// spec: the ones no spec accounts for any more, and the ones this run did
// not verify because it was invoked with different flags.
func reportVariants(spec *load.Spec, ownName string, invoked invocation, report func(bool, string, ...any)) error {
	dir := filepath.Dir(spec.Pos.Filename)
	variants, err := discoverVariants(dir, load.SpecConstraintsIn(spec.InjectorPkg))
	if err != nil {
		return err
	}

	var unverified []string
	for _, v := range variants {
		switch {
		case !v.owned:
			// Not a warning. The file still compiles into whichever build
			// satisfies its constraint, and nothing will ever regenerate
			// it, so it drifts silently from the moment its spec went away.
			report(false, "%s is generated from a spec that no longer exists — delete it, or restore the spec file it came from", v.name)
		case v.name == ownName || v.name == variantFileName(spec.Variant, true):
			// The variant this run is about; already reported above.
		default:
			unverified = append(unverified, fmt.Sprintf("%s (%s)", v.name, invoked.command("generate", v.tags)))
		}
	}
	if len(unverified) > 0 {
		fmt.Printf("  [INFO] not checked by this run, being other variants: %s\n", strings.Join(unverified, ", "))
	}
	return nil
}

// trackedByGit is best-effort: any failure (git missing, not a repo, etc.)
// just means "can't tell", not "problem found".
func trackedByGit(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		rel = path
	}
	cmd := exec.Command("git", "-C", dir, "ls-files", "--error-unmatch", rel)
	return cmd.Run() == nil
}
