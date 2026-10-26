package main

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/okian/servo/v3/internal/emit"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// checkOptions are the flags check takes beyond the load configuration
// every loading command shares.
type checkOptions struct {
	// invoked is the loading flags the run was given, which any command it
	// suggests repeats.
	invoked invocation
}

// runCheck verifies every injector found within dir's scope matches a fresh
// generation, reporting every stale one — not just the first — so a CI job
// checking the whole module surfaces every drifted injector in one run, the
// same way `wire ./... && git diff --exit-code` would. Never rewrites
// anything itself (that's `generate`'s job).
func runCheck(cfg load.Config) error {
	return runCheckWith(cfg, checkOptions{})
}

// runCheckWith is runCheck with check's own flags.
func runCheckWith(cfg load.Config, opts checkOptions) error {
	pipelines, err := buildPipelines(cfg)
	if err != nil {
		return err
	}

	// Resolved once per injector, up front, so the cross-injector config
	// agreement check runs against all of them — a disagreement is a
	// property of the module, and CI should say so even when every
	// individual injector's own file happens to be fresh. check writes
	// nothing, so unlike generate it reports the disagreement and keeps
	// checking rather than stopping.
	resolveds, errs, agreementErr := resolveAll(pipelines)
	if agreementErr != nil {
		errs = append(errs, agreementErr)
	}

	c := newChecker(opts.invoked)
	for i, p := range pipelines {
		if resolveds[i] == nil {
			continue // resolution already failed and is already in errs
		}
		if err := c.checkOne(p, resolveds[i]); err != nil {
			errs = append(errs, err)
		}
		if err := c.checkTestVariant(p); err != nil {
			errs = append(errs, err)
		}
	}
	orphans, err := orphanedCompanions(pipelines)
	if err != nil {
		errs = append(errs, err)
	}
	for _, path := range orphans {
		errs = append(errs, fmt.Errorf("servo check: %s is left over from a //servo:config type its package no longer declares — run %s, which removes it", path, c.invoked.command("generate", cfg.Build.TagList())))
	}
	if err := c.verify(cfg); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// checker compares generated files with a fresh generation, and keeps the
// ones that match so they can be compiled once every comparison is done.
type checker struct {
	invoked invocation
	// current is every generated file found byte-identical to a fresh
	// generation, as committed, and pkgs the packages they sit in.
	current map[string][]byte
	pkgs    []string
}

func newChecker(invoked invocation) *checker {
	return &checker{invoked: invoked, current: map[string][]byte{}}
}

// checkPipeline is resolve + checkOne in one call, for callers (doctor)
// that check a single injector and have no cross-injector concerns.
func checkPipeline(p *pipeline) error {
	return newChecker(invocation{}).checkPipeline(p)
}

func (c *checker) checkPipeline(p *pipeline) error {
	resolved, err := p.resolve(nil)
	if err != nil {
		return err
	}
	return c.checkOne(p, resolved)
}

// checkOne compares one injector's production file and companions.
func (c *checker) checkOne(p *pipeline, resolved *resolve.Resolved) error {
	fresh, err := emit.Emit(resolved, p.spec, false)
	if err != nil {
		return err
	}

	dir := filepath.Dir(p.spec.Pos.Filename)
	name := variantFileName(p.spec.Variant, false)
	// Reported here too, not only by generate: an overlap committed
	// before this check existed, or produced by an older servo, is
	// exactly the thing CI should refuse to let past.
	if err := checkVariantOverlap(dir, name, p.spec.GeneratedConstraint); err != nil {
		return err
	}

	if err := c.compare(filepath.Join(dir, name), fresh, p.spec.Variant, p.spec.InjectorPkg.PkgPath); err != nil {
		return err
	}

	// Companion config loaders are as much this graph's output as the
	// injector file is, and drift the same way: a renamed tag regenerated
	// on one machine and committed without the companion.
	return c.compareCompanions(resolved, p.spec.ConfigFile != nil, p.spec.Variant)
}

// checkTestVariant is checkOne for the override variant, resolved here
// rather than handed in since no cross-injector check needs it. A spec
// with no Override must have no servo-written test variant beside it:
// generate removes one it finds, and check refuses one that lingered —
// it still compiles under go test, and nothing regenerates it.
func (c *checker) checkTestVariant(p *pipeline) error {
	dir := filepath.Dir(p.spec.Pos.Filename)
	path := filepath.Join(dir, variantFileName(p.spec.Variant, true))
	if len(p.spec.Overrides) == 0 {
		if _, ok := servoGeneratedConstraint(path); ok {
			return fmt.Errorf("servo check: %s is left over from a spec that no longer declares servo.Override — run %s, which removes it", path, c.invoked.command("generate", p.spec.Variant))
		}
		return nil
	}
	resolved, err := p.resolve(p.spec.Overrides)
	if err != nil {
		return inTestGraph(p, err)
	}
	fresh, err := emit.Emit(resolved, p.spec, true)
	if err != nil {
		return inTestGraph(p, err)
	}
	if err := c.compare(path, fresh, p.spec.Variant, p.spec.InjectorPkg.PkgPath); err != nil {
		return err
	}
	return c.compareCompanions(resolved, p.spec.ConfigFile != nil, p.spec.Variant)
}

// compare reports a generated file that is missing or differs from fresh,
// naming the command that would produce it, and otherwise records it as
// current, in package pkgPath.
//
// Line endings are not a difference. Git for Windows checks text files out
// with CRLF endings by default (core.autocrlf), so a file committed exactly
// as servo generates it came back different on every byte that ends a
// line: every Windows checkout called every generated file stale, with a
// diff of identical-looking lines and a note blaming another servo
// version.
func (c *checker) compare(path string, fresh []byte, tags []string, pkgPath string) error {
	committed, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("servo check: %s does not exist — run %s", path, c.invoked.command("generate", tags))
		}
		return err
	}
	if lf := bytes.ReplaceAll(committed, []byte("\r\n"), []byte("\n")); !bytes.Equal(lf, fresh) {
		return fmt.Errorf("servo check: %s is stale — run %s\n%s\n%s", path, c.invoked.command("generate", tags), unifiedDiff(string(lf), string(fresh), path), versionSkewNote())
	}
	c.current[path] = committed
	c.pkgs = append(c.pkgs, pkgPath)
	return nil
}

// compareCompanions is compare over every companion loader resolved's
// graph needs, in path order so a report is stable.
func (c *checker) compareCompanions(resolved *resolve.Resolved, withFile bool, tags []string) error {
	companions, err := companionFiles(resolved, withFile)
	if err != nil {
		return err
	}
	pkgOf := make(map[string]string, len(resolved.Configs))
	for _, n := range resolved.Configs {
		pkgOf[filepath.Join(n.Config.Dir, emit.ConfigLoaderFileName)] = n.Config.PkgPath
	}
	for _, path := range slices.Sorted(maps.Keys(companions)) {
		if err := c.compare(path, companions[path], tags, pkgOf[path]); err != nil {
			return err
		}
	}
	return nil
}

// verify compiles the generated files found current. Matching a fresh
// generation byte for byte is not the same as compiling: a Bind to a mock
// whose interface has since grown a method generates the same broken file
// every time, and check used to call it current — doctor too — while
// `go build` failed inside it.
func (c *checker) verify(cfg load.Config) error {
	if len(c.current) == 0 {
		return nil
	}
	v, err := load.Verify(cfg, c.current, c.pkgs)
	if err != nil {
		return fmt.Errorf("servo check: compiling the generated code: %w", err)
	}
	tags := cfg.Build.TagList()
	warnUnchecked(os.Stderr, "servo check", v.Unchecked, c.invoked.command("check", tags))
	if len(v.Problems) == 0 {
		return nil
	}
	return compileFailure("servo check: the generated code does not compile", v.Problems, c.invoked.command("generate", tags))
}

// versionSkewNote is appended to every stale report because the message
// above has exactly one other cause, and no way to tell them apart from
// the diff.
//
// A change to generated code's internal shape is explicitly not a breaking
// change — consumers regenerate — so two machines on two servo versions
// produce a real difference in a file neither of them edited. Without this
// note the loop is: CI says stale, the developer runs the command it names
// with their own binary, pushes, and CI says stale again.
func versionSkewNote() string {
	return "note: this is servo " + version() + ". If regenerating does not settle it, the machine that\n" +
		"      committed the file was running a different version — compare `servo version`, and\n" +
		"      pin one for everybody with `go get -tool " + toolPath + "`."
}

const toolPath = "github.com/okian/servo/v3/cmd/servo"
