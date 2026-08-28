package main

import (
	"fmt"
	"go/token"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// pipeline is everything shared by generate/check/graph/explain/why/list:
// load the module, find the spec, scan candidates, and (only for commands
// that need a fully resolved graph) resolve it.
type pipeline struct {
	loaded     *load.Loaded
	spec       *load.Spec
	candidates []*graph.Provider
	rejected   []graph.Rejected
	caps       *graph.Capabilities
	scope      map[string]bool
}

// loadModule does the spec-independent work shared regardless of how many
// injectors end up being processed: load the package graph once, load
// capabilities once.
func loadModule(dir string) (*load.Loaded, *graph.Capabilities, error) {
	loaded, err := load.Load(load.Config{Dir: dir})
	if err != nil {
		return nil, nil, err
	}
	caps, err := graph.LoadCapabilities(loaded.ServoPkg.Types)
	if err != nil {
		return nil, nil, err
	}
	return loaded, caps, nil
}

func pipelineFor(loaded *load.Loaded, caps *graph.Capabilities, spec *load.Spec) *pipeline {
	candidates, rejected := graph.ScanCandidates(loaded.All, spec.InjectorPkg.PkgPath)
	return &pipeline{
		loaded:     loaded,
		spec:       spec,
		candidates: candidates,
		rejected:   rejected,
		caps:       caps,
		scope:      mainModuleScope(loaded),
	}
}

// buildPipeline resolves exactly one injector — for commands that operate
// on a single target and ask the caller to disambiguate with --dir when
// the scope contains more than one (see load.FindSpec).
func buildPipeline(dir string) (*pipeline, error) {
	loaded, caps, err := loadModule(dir)
	if err != nil {
		return nil, err
	}
	spec, err := load.FindSpec(loaded)
	if err != nil {
		return nil, err
	}
	if err := loaded.NonInjectorErrors(spec.InjectorPkg.PkgPath); err != nil {
		return nil, fmt.Errorf("servo: module has build errors:\n%w", err)
	}
	return pipelineFor(loaded, caps, spec), nil
}

// buildPipelines resolves every injector found within dir's scope — for
// generate/check, which process a whole multi-injector module in one pass
// (matching `wire ./...`'s discovery model) rather than erroring when more
// than one spec exists.
func buildPipelines(dir string) ([]*pipeline, error) {
	loaded, caps, err := loadModule(dir)
	if err != nil {
		return nil, err
	}
	specs, err := load.FindSpecs(loaded)
	if err != nil {
		return nil, err
	}

	injectorPaths := make([]string, len(specs))
	for i, s := range specs {
		injectorPaths[i] = s.InjectorPkg.PkgPath
	}
	// Exclude every known injector's own package, not just "the current
	// one" — otherwise checking injector B trips on injector A's
	// legitimate pre-generation "undefined: New".
	if err := loaded.NonInjectorErrors(injectorPaths...); err != nil {
		return nil, fmt.Errorf("servo: module has build errors:\n%w", err)
	}

	pipelines := make([]*pipeline, len(specs))
	for i, spec := range specs {
		pipelines[i] = pipelineFor(loaded, caps, spec)
	}
	return pipelines, nil
}

// mainModuleScope bounds structural interface search to the main module.
// The risk it guards against is deep, wide *third-party* dependency trees
// producing false ambiguity at scale — not sibling packages within the
// user's own module. An interface implementation deliberately living in a
// package the consumer doesn't import is the entire point of depending on
// an interface, so scope is the whole main module rather than "packages the
// consuming package itself imports" (a narrower rule that would make
// auto-bind useless for exactly that common case). Everything in the main
// module is in scope; stdlib/third-party candidates are not.
func mainModuleScope(loaded *load.Loaded) map[string]bool {
	scope := make(map[string]bool)
	for _, p := range loaded.All {
		if p.Module != nil && p.Module.Main {
			scope[p.PkgPath] = true
		}
	}
	return scope
}

// resolve resolves p's graph, optionally merging extra binds (servotest
// overrides) with priority — returning a formatted, non-nil error listing
// every diagnostic when resolution fails, never a partially resolved graph.
func (p *pipeline) resolve(extraBinds []load.BindDecl) (*resolve.Resolved, error) {
	// Fset/Pkgs are only ever read by scope detection, and a couple of
	// tests build a *pipeline by hand with no loaded module at all, so a
	// missing one is "nothing to inspect", not a nil dereference.
	var fset *token.FileSet
	var pkgs []*packages.Package
	if p.loaded != nil {
		fset, pkgs = p.loaded.Fset, p.loaded.All
	}
	resolved, diags := resolve.Resolve(resolve.Input{
		Spec:       p.spec,
		Candidates: p.candidates,
		Caps:       p.caps,
		Scope:      p.scope,
		ExtraBinds: extraBinds,
		Fset:       fset,
		Pkgs:       pkgs,
	})
	if len(diags) > 0 {
		msg := fmt.Sprintf("servo: %d diagnostic(s):\n", len(diags))
		for _, d := range diags {
			msg += "\n" + d.String()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return resolved, nil
}

// findNode resolves query against a graph's nodes: an exact key match
// first, else a unique suffix match — so "api.Server" finds
// "*example.com/app/api.Server" without the caller typing the full import
// path, but stays precise (an error, not a guess) when that suffix is
// ambiguous.
func findNode(resolved *resolve.Resolved, query string) (*resolve.Node, error) {
	all := allNodes(resolved)
	for _, n := range all {
		if n.Key.String() == query || n.Key.Type == query {
			return n, nil
		}
	}
	var matches []*resolve.Node
	for _, n := range all {
		if strings.HasSuffix(n.Key.Type, query) {
			matches = append(matches, n)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("servo: no node matches %q", query)
	default:
		var names []string
		for _, n := range matches {
			names = append(names, n.Key.String())
		}
		return nil, fmt.Errorf("servo: %q matches multiple nodes, be more specific: %s", query, strings.Join(names, ", "))
	}
}

// allNodes is every resolved node the user could ask about: the app's
// singletons followed by each scope's members. Scoped nodes are kept out
// of Resolved.Order (they are constructed per key, not once by New), but
// `servo explain` and `servo why` are questions about the graph, and a
// scoped node is as much a part of it as any other.
func allNodes(resolved *resolve.Resolved) []*resolve.Node {
	all := append([]*resolve.Node(nil), resolved.Order...)
	for _, s := range resolved.Scopes {
		all = append(all, s.Order...)
	}
	return all
}

func joinOrNone(ss []string) string {
	if len(ss) == 0 {
		return "none"
	}
	return strings.Join(ss, ", ")
}
