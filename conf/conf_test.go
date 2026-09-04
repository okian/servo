package conf

import (
	"strings"
	"testing"
	"time"
)

func TestSection(t *testing.T) {
	if s, err := Section(nil, "db"); s != nil || err != nil {
		t.Fatalf("nil file: %v %v", s, err)
	}
	if s, err := Section(map[string]any{}, "db"); s != nil || err != nil {
		t.Fatalf("absent section: %v %v", s, err)
	}
	if s, err := Section(map[string]any{"db": nil}, "db"); s != nil || err != nil {
		t.Fatalf("null section: %v %v", s, err)
	}
	want := map[string]any{"dsn": "x"}
	if s, err := Section(map[string]any{"db": want}, "db"); err != nil || s["dsn"] != "x" {
		t.Fatalf("present section: %v %v", s, err)
	}
	if _, err := Section(map[string]any{"db": 3}, "db"); err == nil || !strings.Contains(err.Error(), "not a mapping") {
		t.Fatalf("scalar section: %v", err)
	}
}

func TestStringAndBool(t *testing.T) {
	if s, err := String("x"); err != nil || s != "x" {
		t.Fatalf("String: %v %v", s, err)
	}
	if _, err := String(3); err == nil {
		t.Fatal("String(3) should fail — numbers are not silently stringified")
	}
	if b, err := Bool(true); err != nil || !b {
		t.Fatalf("Bool: %v %v", b, err)
	}
	if _, err := Bool("true"); err == nil {
		t.Fatal("Bool(\"true\") should fail")
	}
}

func TestDuration(t *testing.T) {
	if d, err := Duration("90s"); err != nil || d != 90*time.Second {
		t.Fatalf("Duration: %v %v", d, err)
	}
	if _, err := Duration(30); err == nil {
		t.Fatal("a bare number is not a duration")
	}
	// The failing value must never be echoed — a duration field can be
	// tagged secret.
	if _, err := Duration("s3cr3t"); err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("Duration error echoes the value: %v", err)
	}
}

// TestIntegerCoercions covers the one reason this package exists: JSON
// yields float64, yaml.v3 yields int, TOML yields int64 — all for the
// same number in the same file.
func TestIntegerCoercions(t *testing.T) {
	for _, v := range []any{int(40), int64(40), float64(40)} {
		n, err := Int32(v)
		if err != nil || n != 40 {
			t.Fatalf("Int32(%T %v) = %v, %v", v, v, n, err)
		}
	}
	if _, err := Int32(float64(40.5)); err == nil {
		t.Fatal("a fractional float is not an integer")
	}
	if _, err := Int8(300); err == nil || !strings.Contains(err.Error(), "overflows int8") {
		t.Fatalf("Int8(300): %v", err)
	}
	if _, err := Int64("40"); err == nil {
		t.Fatal("a string is not an integer")
	}
	if n, err := Int64(uint64(1 << 62)); err != nil || n != 1<<62 {
		t.Fatalf("Int64(uint64): %v %v", n, err)
	}
	if _, err := Int64(uint64(1) << 63); err == nil {
		t.Fatal("uint64 above MaxInt64 must not wrap")
	}
}

func TestUnsignedCoercions(t *testing.T) {
	for _, v := range []any{int(40), int64(40), float64(40), uint64(40)} {
		n, err := Uint16(v)
		if err != nil || n != 40 {
			t.Fatalf("Uint16(%T %v) = %v, %v", v, v, n, err)
		}
	}
	if _, err := Uint16(-1); err == nil {
		t.Fatal("negative must not wrap to unsigned")
	}
	if _, err := Uint8(300); err == nil || !strings.Contains(err.Error(), "overflows uint8") {
		t.Fatalf("Uint8(300): %v", err)
	}
}

func TestFloatCoercions(t *testing.T) {
	if f, err := Float64(2.5); err != nil || f != 2.5 {
		t.Fatalf("Float64: %v %v", f, err)
	}
	if f, err := Float64(int64(3)); err != nil || f != 3 {
		t.Fatalf("Float64(int64): %v %v", f, err)
	}
	if f, err := Float32(2.5); err != nil || f != 2.5 {
		t.Fatalf("Float32: %v %v", f, err)
	}
	if _, err := Float32(1e300); err == nil {
		t.Fatal("1e300 must not land on +Inf")
	}
	if _, err := Float64("2.5"); err == nil {
		t.Fatal("a string is not a float")
	}
}

// TestErrorsNameTypesNotValues pins the redaction contract: config files
// hold secrets, so no coercion error may echo the value it rejected.
func TestErrorsNameTypesNotValues(t *testing.T) {
	secret := "hunter2"
	for name, err := range map[string]error{
		"String": second(String(3)),
		"Int64":  second(Int64(secret)),
		"Bool":   second(Bool(secret)),
	} {
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "3") && name == "String" {
			t.Errorf("%s error echoes the value: %v", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }
