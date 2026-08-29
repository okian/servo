//go:build prod

package main

import (
	"context"
	"testing"
)

// TestProdVariantWiresPostgres is the mirror of app_test.go, gated on the
// same tag as the variant it describes. Testing a build variant means
// running the suite once per configuration — `go test ./...` and
// `go test -tags=prod ./...` — exactly as you would for any other
// tag-gated code.
func TestProdVariantWiresPostgres(t *testing.T) {
	app, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())

	types := nodeTypes(app)
	if !containsSuffix(types, "postgres.DB") {
		t.Errorf("prod build should wire postgres.DB, got %v", types)
	}
	if containsSuffix(types, "memory.Mem") {
		t.Errorf("prod build must not wire memory.Mem, got %v", types)
	}
}
