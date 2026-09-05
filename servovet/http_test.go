package servovet

import (
	"strings"
	"testing"
)

func TestFlagsHTTPMarkerCall(t *testing.T) {
	const src = `package fixture

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.HTTP(),
	)
}
`
	got := runOn(t, src)
	if len(got) != 2 {
		t.Fatalf("got %d diagnostics, want 2 (Build and the nested HTTP): %v", len(got), got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"servo.Build", "servo.HTTP"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diagnostics %v do not mention %q", got, want)
		}
	}
}

// Every new HTTP marker is a panic waiting for an untagged file, exactly
// like the originals — this test is what notices one being forgotten in
// markerNames.
func TestFlagsGroupUseRouteExtractMarkerCalls(t *testing.T) {
	const src = `package fixture

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.HTTP(
			servo.Group("telemetry"),
			servo.Use[int](servo.Route("GET /x")),
		),
		servo.Extract[int](),
	)
}
`
	got := runOn(t, src)
	// Build, HTTP, Group, Use, Route, Extract — six calls, six diagnostics.
	if len(got) != 6 {
		t.Fatalf("got %d diagnostics, want 6: %v", len(got), got)
	}
}

// A malformed //servo: directive gets an in-editor squiggle, mirroring the
// generate-time rejection — the reserved prefix must never silently no-op,
// and the editor is where the typo is cheapest to fix.
func TestFlagsMalformedDirectives(t *testing.T) {
	const src = `package fixture

import "context"

type Resp struct{}

//servo:pots /broken
func Broken(ctx context.Context) (*Resp, error) { return nil, nil }

//servo:get /fine telemetry
func Fine(ctx context.Context) (*Resp, error) { return nil, nil }

// an ordinary comment mentioning //servo: mid-line is not a directive
func Unrelated() {}
`
	got := runOn(t, src)
	if len(got) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %v", len(got), got)
	}
	// servovet sees both directive families, so an unknown //servo: name
	// is reported with the one message that names them both — config on a
	// type, the route verbs on a function.
	if !strings.Contains(got[0], "//servo:pots") || !strings.Contains(got[0], "route verbs") {
		t.Fatalf("diagnostic = %q", got[0])
	}
}
