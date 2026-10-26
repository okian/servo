package graph

import (
	"fmt"
	"go/types"
	"strings"
)

// ServoPackagePath is this module's runtime package, whose capability
// interfaces are the structural shapes every provider's result type is
// checked against.
const ServoPackagePath = "github.com/okian/servo/v3/servo"

// AllCapabilities is the fixed, stable-ordered set of capability names
// Capabilities.Detect can return.
var AllCapabilities = []string{"Initializer", "Runner", "Drainer", "Flusher", "Finalizer", "Healther", "Readier"}

// capabilityMethods names the one method each capability interface
// declares. Every one has the same shape, Method(ctx context.Context)
// error, which is what lets a near miss be recognized without the runtime
// package at hand — servovet has only the package it is analysing.
var capabilityMethods = map[string]string{
	"Initializer": "Init",
	"Runner":      "Run",
	"Drainer":     "Drain",
	"Flusher":     "Flush",
	"Finalizer":   "Stop",
	"Healther":    "Health",
	"Readier":     "Ready",
}

// Capabilities holds the servo package's capability interfaces, loaded once
// per generation so every candidate's result type can be checked against
// them via types.Implements — never via a runtime type assertion.
type Capabilities struct {
	byName map[string]*types.Interface
	// missing is every name in AllCapabilities the loaded runtime does not
	// declare, and pkgPath the runtime package's path, both for Warning.
	missing []string
	pkgPath string
}

// LoadCapabilities extracts the capability interfaces from the loaded servo
// runtime package.
//
// The runtime is the module's dependency, pinned in its go.mod, while the
// capability list is the CLI's, so the two can disagree: a CLI that knows a
// capability added after the release the module pins finds no declaration
// for it. That skips the capability — Detect never reports it, and nothing
// generated calls the method — with Warning saying which, rather than
// failing every command over one interface the module's code cannot be
// using, since its runtime has never heard of it.
//
// Two things are still errors. A declaration of the wrong kind is not an
// older runtime but a different package. And a runtime declaring none of
// them is not one that predates a capability — all seven have existed
// since the first generator release — but one that did not load, which a
// warning followed by an empty graph would only hide.
func LoadCapabilities(servoPkg *types.Package) (*Capabilities, error) {
	c := &Capabilities{byName: make(map[string]*types.Interface, len(AllCapabilities)), pkgPath: servoPkg.Path()}
	for _, name := range AllCapabilities {
		obj := servoPkg.Scope().Lookup(name)
		if obj == nil {
			c.missing = append(c.missing, name)
			continue
		}
		iface, ok := obj.Type().Underlying().(*types.Interface)
		if !ok {
			return nil, fmt.Errorf("graph: %s.%s is not an interface", servoPkg.Path(), name)
		}
		c.byName[name] = iface
	}
	if len(c.byName) == 0 {
		return nil, fmt.Errorf("graph: %s declares none of the capability interfaces (%s) — the package did not load; check that go.mod requires github.com/okian/servo/v3 and that `go mod tidy` has filled go.sum", servoPkg.Path(), strings.Join(AllCapabilities, ", "))
	}
	return c, nil
}

// Missing names the capabilities the loaded runtime does not declare, in
// AllCapabilities order; empty when the runtime and the CLI agree.
func (c *Capabilities) Missing() []string {
	return c.missing
}

// Warning is the one-line notice a command prints when the runtime lacks a
// capability this CLI detects, or "" when nothing is missing. It names the
// likely cause — a module pinning a servo release older than the CLI — and
// the fix, because a method that silently stops being called is the only
// symptom the user would otherwise see.
//
// cliVersion is the running servo's version and runtimeVersion the one the
// module's go.mod resolves; either may be "" when it is not known (a
// development build, a replace directive), and is then left out.
func (c *Capabilities) Warning(cliVersion, runtimeVersion string) string {
	if len(c.missing) == 0 {
		return ""
	}
	runtime := c.pkgPath
	if runtimeVersion != "" {
		runtime += " " + runtimeVersion
	}
	cli := "this servo CLI"
	if cliVersion != "" {
		cli = "servo " + cliVersion
	}
	what := fmt.Sprintf("declares no %s, a capability %s detects", c.missing[0], cli)
	if len(c.missing) > 1 {
		what = fmt.Sprintf("declares none of %s, capabilities %s detects", strings.Join(c.missing, ", "), cli)
	}
	return fmt.Sprintf("servo: warning: %s %s — the module requires an older servo than the CLI it is generated with, so generated code calls no such method until the two match: run the CLI the module pins (`go tool servo`), or update the module (`go get github.com/okian/servo/v3@latest`)", runtime, what)
}

// EmptyCapabilities returns a Capabilities that detects nothing. It exists
// for callers (notably internal/resolve's tests) that need a non-nil
// Capabilities but aren't testing capability detection itself, so they
// don't have to load the real servo package via go/packages.
func EmptyCapabilities() *Capabilities {
	return &Capabilities{byName: make(map[string]*types.Interface)}
}

// Detect returns the names of every capability t structurally implements,
// in AllCapabilities order.
func (c *Capabilities) Detect(t types.Type) []string {
	var got []string
	for _, name := range AllCapabilities {
		iface, ok := c.byName[name]
		if !ok {
			continue // EmptyCapabilities or a partially loaded set: nothing to detect
		}
		if types.Implements(t, iface) {
			got = append(got, name)
		}
	}
	return got
}

// CapabilityNearMiss is a method that carries a capability's name without
// conferring it: the wrong signature, or a pointer-receiver method on a
// node that is the value type. Detection is types.Implements and nothing
// else, so servo never calls such a method — `Close() error`-style
// spellings, a Run that forgot its context, a Stop with no error result
// all left a component reported as "capabilities: none" with nothing said.
type CapabilityNearMiss struct {
	Capability string      // "Runner"
	Method     *types.Func // the method as declared
	// Problem says what is wrong with it, as a clause:
	// "has the signature Run() error, not Run(ctx context.Context) error".
	Problem string
}

// CapabilityNearMisses reports every capability-named method t has that
// does not make t implement the capability, in AllCapabilities order. It
// needs no loaded runtime package: every capability's one method has the
// shape Method(ctx context.Context) error.
func CapabilityNearMisses(t types.Type) []CapabilityNearMiss {
	var out []CapabilityNearMiss
	for _, name := range AllCapabilities {
		method := capabilityMethods[name]
		want := method + "(ctx context.Context) error"
		if fn := lookupMethod(t, method); fn != nil {
			if !isCapabilitySignature(fn.Type().(*types.Signature)) {
				out = append(out, CapabilityNearMiss{Capability: name, Method: fn, Problem: fmt.Sprintf("has the signature %s, not %s", signatureString(fn), want)})
			}
			continue
		}
		// The value type's method set leaves out pointer-receiver methods,
		// so a constructor returning T rather than *T hides every one of
		// them from types.Implements.
		if _, isPtr := types.Unalias(t).(*types.Pointer); isPtr {
			continue
		}
		if fn := lookupMethod(types.NewPointer(t), method); fn != nil && isCapabilitySignature(fn.Type().(*types.Signature)) {
			out = append(out, CapabilityNearMiss{Capability: name, Method: fn, Problem: fmt.Sprintf("is declared on the pointer receiver, but the node is the value %s, whose method set does not include it — return *%s from the constructor", TypeString(t), TypeString(t))})
		}
	}
	return out
}

// UnstoppedCloser returns t's Close or Shutdown method when t has no
// Stop(ctx context.Context) error, or nil. At shutdown servo calls Drain,
// Flush and Stop and nothing else, so a component that releases its
// resources in a Close — *sql.DB's shape, and http.Server's Shutdown — is
// never released by servo unless something bridges the two.
func UnstoppedCloser(t types.Type) *types.Func {
	if stop := lookupMethod(t, capabilityMethods["Finalizer"]); stop != nil && isCapabilitySignature(stop.Type().(*types.Signature)) {
		return nil
	}
	for _, name := range []string{"Close", "Shutdown"} {
		if fn := lookupMethod(t, name); fn != nil {
			return fn
		}
	}
	return nil
}

// lookupMethod is t's method named name — declared or promoted — or nil.
func lookupMethod(t types.Type, name string) *types.Func {
	obj, _, _ := types.LookupFieldOrMethod(t, false, nil, name)
	fn, _ := obj.(*types.Func)
	return fn
}

func isCapabilitySignature(sig *types.Signature) bool {
	return !sig.Variadic() &&
		sig.Params().Len() == 1 && IsContextType(sig.Params().At(0).Type()) &&
		sig.Results().Len() == 1 && IsErrorType(sig.Results().At(0).Type())
}

// signatureString renders fn as `Name(params) results` with package-local
// type names, the way the reader wrote it.
func signatureString(fn *types.Func) string {
	sig := fn.Type().(*types.Signature)
	s := types.TypeString(types.NewSignatureType(nil, nil, nil, sig.Params(), sig.Results(), sig.Variadic()), func(p *types.Package) string { return p.Name() })
	return fn.Name() + strings.TrimPrefix(s, "func")
}
