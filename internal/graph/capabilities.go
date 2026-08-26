package graph

import (
	"fmt"
	"go/types"
)

// ServoPackagePath is this module's runtime package, whose capability
// interfaces are the structural shapes every provider's result type is
// checked against.
const ServoPackagePath = "github.com/okian/servo/v2/servo"

// AllCapabilities is the fixed, stable-ordered set of capability names
// Capabilities.Detect can return.
var AllCapabilities = []string{"Initializer", "Runner", "Drainer", "Flusher", "Finalizer", "Healther", "Readier"}

// Capabilities holds the servo package's capability interfaces, loaded once
// per generation so every candidate's result type can be checked against
// them via types.Implements — never via a runtime type assertion.
type Capabilities struct {
	byName map[string]*types.Interface
}

// LoadCapabilities extracts the seven capability interfaces from the loaded
// servo runtime package.
func LoadCapabilities(servoPkg *types.Package) (*Capabilities, error) {
	c := &Capabilities{byName: make(map[string]*types.Interface, len(AllCapabilities))}
	for _, name := range AllCapabilities {
		obj := servoPkg.Scope().Lookup(name)
		if obj == nil {
			return nil, fmt.Errorf("graph: capability interface %s not found in %s", name, servoPkg.Path())
		}
		iface, ok := obj.Type().Underlying().(*types.Interface)
		if !ok {
			return nil, fmt.Errorf("graph: %s.%s is not an interface", servoPkg.Path(), name)
		}
		c.byName[name] = iface
	}
	return c, nil
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
