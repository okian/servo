package servo

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestStatusCodesAndText(t *testing.T) {
	cases := []struct {
		status HTTPStatus
		code   int
		text   string
	}{
		{Status.OK, 200, "OK"},
		{Status.CREATED, 201, "Created"},
		{Status.NO_CONTENT, 204, "No Content"},
		{Status.BAD_REQUEST, 400, "Bad Request"},
		{Status.FORBIDDEN, 403, "Forbidden"},
		{Status.NOT_FOUND, 404, "Not Found"},
		// RFC 9110 renamed these; servo uses the current names and text,
		// deliberately diverging from net/http's pre-9110 strings.
		{Status.CONTENT_TOO_LARGE, 413, "Content Too Large"},
		{Status.UNPROCESSABLE_CONTENT, 422, "Unprocessable Content"},
		{Status.INTERNAL_SERVER_ERROR, 500, "Internal Server Error"},
		{Status.NETWORK_AUTHENTICATION_REQUIRED, 511, "Network Authentication Required"},
	}
	for _, c := range cases {
		if got := c.status.Code(); got != c.code {
			t.Errorf("Code() = %d, want %d", got, c.code)
		}
		if got := c.status.Error(); got != c.text {
			t.Errorf("Error() for %d = %q, want %q", c.code, got, c.text)
		}
	}
}

// Every field of the Status table must carry a real registered code and a
// non-empty canonical text, and no two fields may share a code. reflect is
// fine here: it is a test, not the runtime, and the runtime's no-reflection
// rule is about generated code paths.
func TestStatusTableComplete(t *testing.T) {
	v := reflect.ValueOf(Status)
	typ := v.Type()
	seen := map[int]string{}
	for i := 0; i < v.NumField(); i++ {
		name := typ.Field(i).Name
		s, ok := v.Field(i).Interface().(HTTPStatus)
		if !ok {
			t.Fatalf("Status.%s is not an HTTPStatus", name)
		}
		code := s.Code()
		if code < 100 || code > 599 {
			t.Errorf("Status.%s has out-of-range code %d", name, code)
		}
		if s.Error() == "" || s.Error() == fmt.Sprintf("HTTP %d", code) {
			t.Errorf("Status.%s (%d) has no canonical text", name, code)
		}
		if prior, dup := seen[code]; dup {
			t.Errorf("Status.%s and Status.%s share code %d", name, prior, code)
		}
		seen[code] = name
	}
	if len(seen) < 40 {
		t.Errorf("Status table has %d entries, expected the full RFC 9110 + IANA set", len(seen))
	}
}

func TestStatusNew(t *testing.T) {
	err := Status.FORBIDDEN.New("category is closed")
	if got := err.Error(); got != "category is closed" {
		t.Fatalf("Error() = %q, want the user's message verbatim", got)
	}
	var hs HTTPStatus
	if !errors.As(err, &hs) || hs.Code() != 403 {
		t.Fatalf("errors.As gave %+v, want 403", hs)
	}
}

func TestStatusNewf(t *testing.T) {
	err := Status.BAD_REQUEST.Newf("page %d out of range", 42)
	if got := err.Error(); got != "page 42 out of range" {
		t.Fatalf("Error() = %q", got)
	}
	var hs HTTPStatus
	if !errors.As(err, &hs) || hs.Code() != 400 {
		t.Fatalf("errors.As gave %+v, want 400", hs)
	}
}

func TestStatusWrapf(t *testing.T) {
	sentinel := errors.New("boom")
	err := Status.NOT_FOUND.Wrapf("category %q: %w", "espresso", sentinel)

	if got, want := err.Error(), `category "espresso": boom`; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("wrapped sentinel must survive errors.Is")
	}
	var hs HTTPStatus
	if !errors.As(err, &hs) || hs.Code() != 404 {
		t.Fatalf("errors.As gave %+v, want 404", hs)
	}
	// The status itself is in the chain too, so callers can match on the
	// table entry directly.
	if !errors.Is(err, Status.NOT_FOUND) {
		t.Fatalf("errors.Is(err, Status.NOT_FOUND) must hold")
	}
	if errors.Is(err, Status.FORBIDDEN) {
		t.Fatalf("errors.Is must not match a different status")
	}
}

func TestStatusWrap(t *testing.T) {
	sentinel := errors.New("db down")
	err := Status.INTERNAL_SERVER_ERROR.Wrap(sentinel)
	if got := err.Error(); got != "db down" {
		t.Fatalf("Error() = %q, want the wrapped message verbatim", got)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Wrap must keep the wrapped error in the chain")
	}
	var hs HTTPStatus
	if !errors.As(err, &hs) || hs.Code() != 500 {
		t.Fatalf("errors.As gave %+v, want 500", hs)
	}
}

// Wrap(nil) returns the bare status rather than an error whose message
// would panic — the same shape as returning servo.Status.CREATED directly.
func TestStatusWrapNil(t *testing.T) {
	err := Status.CREATED.Wrap(nil)
	var hs HTTPStatus
	if !errors.As(err, &hs) || hs.Code() != 201 {
		t.Fatalf("Wrap(nil) = %v, want the bare 201 status", err)
	}
	if got := err.Error(); got != "Created" {
		t.Fatalf("Error() = %q", got)
	}
}

// The success-as-error contract: a bare table entry in the error position
// is how a handler picks a non-200 success code. The generated adapter
// recovers it with errors.As and reads Code().
func TestStatusBareValueAsError(t *testing.T) {
	var err error = Status.CREATED
	var hs HTTPStatus
	if !errors.As(err, &hs) || hs.Code() != 201 {
		t.Fatalf("bare status in error position gave %+v", hs)
	}
}
