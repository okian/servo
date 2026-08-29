//go:build !prod

package main

import (
	"context"
	"testing"
)

// TestDefaultVariantWiresMemory asserts the graph this configuration
// actually built. Graph() is generated from the resolved plan, so it is the
// public way to see which implementation a variant selected — no reaching
// into App's unexported fields.
func TestDefaultVariantWiresMemory(t *testing.T) {
	app, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())

	types := nodeTypes(app)
	if !containsSuffix(types, "memory.Mem") {
		t.Errorf("default build should wire memory.Mem, got %v", types)
	}
	if containsSuffix(types, "postgres.DB") {
		t.Errorf("default build must not wire postgres.DB, got %v", types)
	}
}
