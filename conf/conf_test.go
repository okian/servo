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

// TestEverySizedCoercion runs each sized integer coercion over every input
// type a decoder can hand it — TOML and YAML produce sized integers, JSON
// only float64 — plus the bounds where each one must refuse rather than
// wrap. Int, Int16, Uint and Uint32 had no test at all before.
func TestEverySizedCoercion(t *testing.T) {
	inputs := []any{int(7), int8(7), int16(7), int32(7), int64(7), uint(7), uint8(7), uint16(7), uint32(7), uint64(7), float64(7)}
	signed := map[string]func(any) (int64, error){
		"Int":   func(v any) (int64, error) { n, err := Int(v); return int64(n), err },
		"Int8":  func(v any) (int64, error) { n, err := Int8(v); return int64(n), err },
		"Int16": func(v any) (int64, error) { n, err := Int16(v); return int64(n), err },
		"Int32": func(v any) (int64, error) { n, err := Int32(v); return int64(n), err },
		"Int64": Int64,
	}
	unsigned := map[string]func(any) (uint64, error){
		"Uint":   func(v any) (uint64, error) { n, err := Uint(v); return uint64(n), err },
		"Uint8":  func(v any) (uint64, error) { n, err := Uint8(v); return uint64(n), err },
		"Uint16": func(v any) (uint64, error) { n, err := Uint16(v); return uint64(n), err },
		"Uint32": func(v any) (uint64, error) { n, err := Uint32(v); return uint64(n), err },
		"Uint64": Uint64,
	}
	for _, v := range inputs {
		for name, fn := range signed {
			if n, err := fn(v); err != nil || n != 7 {
				t.Errorf("%s(%T 7) = %d, %v", name, v, n, err)
			}
		}
		for name, fn := range unsigned {
			if n, err := fn(v); err != nil || n != 7 {
				t.Errorf("%s(%T 7) = %d, %v", name, v, n, err)
			}
		}
	}

	refusals := []struct {
		name string
		call func() error
		want string
	}{
		{"Int16 above range", func() error { _, err := Int16(int64(40000)); return err }, "overflows int16"},
		{"Int16 below range", func() error { _, err := Int16(int64(-40000)); return err }, "overflows int16"},
		{"Int32 above range", func() error { _, err := Int32(int64(1) << 40); return err }, "overflows int32"},
		{"Uint32 above range", func() error { _, err := Uint32(uint64(1) << 40); return err }, "overflows uint32"},
		{"Uint negative", func() error { _, err := Uint(int64(-1)); return err }, "negative value"},
		{"Uint32 negative float", func() error { _, err := Uint32(float64(-3)); return err }, "not an integer that fits"},
		{"Uint64 fractional", func() error { _, err := Uint64(float64(2.5)); return err }, "not an integer that fits"},
		{"Int string", func() error { _, err := Int("7"); return err }, "cannot use string as integer"},
		{"Uint bool", func() error { _, err := Uint(true); return err }, "cannot use bool as integer"},
		{"Float32 overflow", func() error { _, err := Float32(float64(1e300)); return err }, "overflows float32"},
		{"Float64 string", func() error { _, err := Float64("1.5"); return err }, "cannot use string as float"},
	}
	for _, r := range refusals {
		err := r.call()
		if err == nil || !strings.Contains(err.Error(), r.want) {
			t.Errorf("%s: err = %v, want it to contain %q", r.name, err, r.want)
		}
	}

	for _, v := range []any{float32(1.5), int8(-2), uint16(3)} {
		if _, err := Float64(v); err != nil {
			t.Errorf("Float64(%T): %v", v, err)
		}
	}
}
