package main

import (
	"fmt"
	"go/token"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
	"github.com/okian/servo/v3/internal/route"
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
	// routes is the module's //servo: route directives, scanned once per
	// load like capabilities: a directive names an exported handler, so
	// which injector serves it is not the scan's question.
	routes []*route.Route
	// configs is every //servo:config type in the module, scanned once.
	configs []*graph.ConfigDecl
}

// loadModule does the spec-independent work shared regardless of how many
// injectors end up being processed: load the package graph once, load
// capabilities once, scan //servo: route directives and //servo:config
// declarations once. A malformed directive fails the load outright — the
// //servo: prefix is reserved, and a typo silently dropping a route or a
// config setting is the failure mode the reservation prevents.
func loadModule(cfg load.Config) (*load.Loaded, *graph.Capabilities, []*route.Route, []*graph.ConfigDecl, error) {
	loaded, err := load.Load(cfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	caps, err := graph.LoadCapabilities(loaded.ServoPkg.Types)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	routes, rdiags := route.Scan(loaded.All, loaded.ServoPkg.Types)
	if len(rdiags) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "servo: %d diagnostic(s):\n", len(rdiags))
		for _, d := range rdiags {
			b.WriteString("\n" + d.String())
		}
		return nil, nil, nil, nil, fmt.Errorf("%s", b.String())
	}
	configs, err := graph.ScanConfigs(loaded.All)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return loaded, caps, routes, configs, nil
}

func pipelineFor(loaded *load.Loaded, caps *graph.Capabilities, routes []*route.Route, configs []*graph.ConfigDecl, spec *load.Spec) *pipeline {
	candidates, rejected := graph.ScanCandidates(loaded.All, spec.InjectorPkg.PkgPath)
	return &pipeline{
		loaded:     loaded,
		spec:       spec,
		candidates: candidates,
		rejected:   rejected,
		caps:       caps,
		scope:      mainModuleScope(loaded),
		routes:     routes,
		configs:    configs,
	}
}

// buildPipeline resolves exactly one injector — for commands that operate
// on a single target and ask the caller to disambiguate with --dir when
// the scope contains more than one (see load.FindSpec).
func buildPipeline(cfg load.Config) (*pipeline, error) {
	loaded, caps, routes, configs, err := loadModule(cfg)
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
	if err := checkRouteGroups([]*load.Spec{spec}, routes); err != nil {
		return nil, err
	}
	return pipelineFor(loaded, caps, routes, configs, spec), nil
}

// buildPipelines resolves every injector found within dir's scope — for
// generate/check, which process a whole multi-injector module in one pass
// (matching `wire ./...`'s discovery model) rather than erroring when more
// than one spec exists.
func buildPipelines(cfg load.Config) ([]*pipeline, error) {
	loaded, caps, routes, configs, err := loadModule(cfg)
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

	if err := checkRouteGroups(specs, routes); err != nil {
		return nil, err
	}

	pipelines := make([]*pipeline, len(specs))
	for i, spec := range specs {
		pipelines[i] = pipelineFor(loaded, caps, routes, configs, spec)
	}
	return pipelines, nil
}

// checkRouteGroups errors when a route names a group no spec declares —
// the typo that would otherwise silently leave the route unserved. It is a
// module-wide check, not a per-injector one: an injector legitimately skips
// groups it does not declare, but a group nobody declares is a mistake.
func checkRouteGroups(specs []*load.Spec, routes []*route.Route) error {
	declared := map[string]bool{}
	for _, s := range specs {
		if s.HTTP == nil {
			continue
		}
		for _, g := range s.HTTP.Groups {
			declared[g.Name] = true
		}
	}
	var msgs []string
	for _, rt := range routes {
		if rt.Group == "" || declared[rt.Group] {
			continue
		}
		msgs = append(msgs, fmt.Sprintf("%s: servo: group %q is not declared by any injector — declare it with servo.Group(%q) inside servo.HTTP(...)", rt.Pos, rt.Group, rt.Group))
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("servo: %d diagnostic(s):\n\n%s", len(msgs), strings.Join(msgs, "\n"))
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

// resolveAll resolves every pipeline's production graph and runs the one
// cross-injector check (config agreement) against all of them. resolveds
// is parallel to pipelines, nil where resolution failed; those failures
// are in errs, each prefixed with its injector's package path so a
// multi-injector report says which graph broke. agreementErr is returned
// separately because the two callers treat it differently: generate must
// refuse to write anything on a disagreement, while check — which writes
// nothing — reports it and keeps checking.
func resolveAll(pipelines []*pipeline) (resolveds []*resolve.Resolved, errs []error, agreementErr error) {
	resolveds = make([]*resolve.Resolved, len(pipelines))
	for i, p := range pipelines {
		resolved, err := p.resolve(nil)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.spec.InjectorPkg.PkgPath, err))
			continue
		}
		resolveds[i] = resolved
	}
	return resolveds, errs, checkConfigAgreement(pipelines, resolveds)
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
	// The HTTP input exists only for specs that declared servo.HTTP() —
	// and needs a loaded module for the *servo.HTTPConfig key, which the
	// couple of tests that hand-build a pipeline never have.
	var httpIn *resolve.HTTPInput
	if p.spec.HTTP != nil && p.loaded != nil {
		configKey, configType := route.HTTPConfigKey(p.loaded.ServoPkg.Types)
		httpIn = &resolve.HTTPInput{
			Pos:        p.spec.HTTP.Pos,
			Routes:     p.routes,
			Groups:     p.spec.HTTP.Groups,
			Uses:       p.spec.HTTP.Uses,
			Extracts:   p.spec.Extracts,
			ConfigKey:  configKey,
			ConfigType: configType,
		}
	}
	resolved, diags := resolve.Resolve(resolve.Input{
		Spec:       p.spec,
		Candidates: p.candidates,
		Caps:       p.caps,
		Scope:      p.scope,
		ExtraBinds: extraBinds,
		Configs:    p.configs,
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpIn,
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
	// Supplied values and configs are nodes the graph genuinely contains,
	// so `explain` and `why` have to find them: a type the app depends on
	// that these commands report as unknown is worse than not supporting
	// them.
	all = append(all, resolved.Supplied...)
	return append(all, resolved.Configs...)
}

func joinOrNone(ss []string) string {
	if len(ss) == 0 {
		return "none"
	}
	return strings.Join(ss, ", ")
}

// moduleRoot is the directory generated positions are written relative to.
// `servo graph` reports the same strings the generated App.Graph() carries
// only if it trims the same prefix.
func moduleRoot(spec *load.Spec) string {
	if mod := spec.InjectorPkg.Module; mod != nil {
		return mod.Dir
	}
	return ""
}
