package servovet

import (
	"strings"
	"testing"
)

// A route directive in the right place can still be malformed, and is
// flagged with the message `servo generate` prints for it: no pattern, a
// pattern that is not a path, a stray third word, a group outside the
// grammar, or a pattern net/http itself refuses.
func TestFlagsMalformedRouteDirectives(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"//servo:post", "needs a route pattern"},
		{"//servo:get orders", `route pattern "orders" must start with "/"`},
		{"//servo:get /orders telemetry extra", "takes a pattern and an optional group"},
		{"//servo:get /orders back!office", `group "back!office" must match`},
		{"//servo:get /orders/{id", "cannot be registered on net/http.ServeMux"},
	} {
		src := "package fixture\n\nimport \"context\"\n\ntype Resp struct{}\n\n" +
			tc.line + "\nfunc Orders(ctx context.Context) (*Resp, error) { return nil, nil }\n"
		got := runOn(t, src)
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: got %v, want one diagnostic containing %q", tc.line, got, tc.want)
		}
	}
}

// A near miss is a line comment one space away from a well-formed
// directive. A block comment is never a directive, with the space or
// without it, and a name servo does not know would still be malformed
// once the space went away; neither is worth a squiggle.
func TestNearMissNeedsAWellFormedLineComment(t *testing.T) {
	const src = `package fixture

/* servo:get /orders */
func Orders() {}

// servo:frobnicate /orders
func Frobnicate() {}
`
	if got := runOn(t, src); len(got) != 0 {
		t.Fatalf("got %v, want nothing", got)
	}
}

// A call through an expression — a func literal, the func another call
// returns — names no function, so it cannot be a marker; the marker call
// beside them still is.
func TestCallsThroughExpressionsAreNotMarkers(t *testing.T) {
	const src = `package fixture

import "github.com/okian/servo/v3/servo"

func pick() func() { return func() {} }

func wire() {
	func() {}()
	pick()()
	servo.Build()
}
`
	got := runOn(t, src)
	if len(got) != 1 || !strings.Contains(got[0], "servo.Build") {
		t.Fatalf("got %v, want only servo.Build flagged", got)
	}
}
