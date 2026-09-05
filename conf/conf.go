// Package conf is the small runtime behind generated //servo:config
// loaders when a servo.ConfigFile is declared: coercions from the untyped
// values a decoded config file yields (map[string]any) to the exact field
// types a config struct declares.
//
// It exists because the three decoders disagree about numbers — JSON
// yields float64 for every number, yaml.v3 yields int or float64, TOML
// yields int64 or float64 — and that normalization deserves to be written
// and tested once rather than stamped as a type switch into every
// generated loader. It is deliberately stdlib-only: format decoding is the
// generated code's business (which is how an env-only app carries no
// decoder at all), and this package never learns which format produced the
// value it is handed.
//
// Error messages name types, never values: a config file holds secrets,
// and an error that echoes one into a log defeats the `secret` tag.
package conf

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Section returns the mapping under key, nil when the file (or the key) is
// absent — indexing a nil map is fine, so a generated loader treats "no
// file" and "no section" identically — and an error when the key holds
// anything other than a mapping, which is a malformed file worth saying so
// about rather than silently reading zero settings from.
func Section(m map[string]any, key string) (map[string]any, error) {
	if m == nil {
		return nil, nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil, nil
	}
	section, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("conf: section %q is not a mapping (got %T)", key, v)
	}
	return section, nil
}

// String coerces a decoded file value to string. Only a real string is
// accepted: quietly stringifying a number the author meant as one would
// hide a typo'd field type.
func String(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("conf: cannot use %T as string", v)
	}
	return s, nil
}

// Bool coerces a decoded file value to bool.
func Bool(v any) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("conf: cannot use %T as bool", v)
	}
	return b, nil
}

// Duration coerces a decoded file value to time.Duration. Durations are
// written as strings ("30m", "1h15m") in every format; a bare number would
// be nanoseconds, which nobody means.
func Duration(v any) (time.Duration, error) {
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("conf: cannot use %T as duration (write it as a string, e.g. \"30m\")", v)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		// time.ParseDuration echoes the value in its error; a duration field
		// can be tagged secret, so report the failure without it.
		return 0, errors.New("conf: not a valid duration string")
	}
	return d, nil
}

// Int64 coerces a decoded file value to int64: any integer type a decoder
// produces, or a float64 that is exactly integral (JSON has no integer
// type at all, so 40 arrives as 40.0).
func Int64(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int8:
		return int64(n), nil
	case int16:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint:
		if uint64(n) > math.MaxInt64 {
			return 0, errors.New("conf: value overflows int64")
		}
		return int64(n), nil
	case uint8:
		return int64(n), nil
	case uint16:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		if n > math.MaxInt64 {
			return 0, errors.New("conf: value overflows int64")
		}
		return int64(n), nil
	case float64:
		if n != math.Trunc(n) || n < math.MinInt64 || n >= math.MaxInt64 {
			return 0, errors.New("conf: value is not an integer that fits int64")
		}
		return int64(n), nil
	default:
		return 0, fmt.Errorf("conf: cannot use %T as integer", v)
	}
}

// Int coerces to the platform int, via Int64.
func Int(v any) (int, error) {
	n, err := int64InRange(v, math.MinInt, math.MaxInt, "int")
	return int(n), err
}

// Int8 coerces to int8, via Int64.
func Int8(v any) (int8, error) {
	n, err := int64InRange(v, math.MinInt8, math.MaxInt8, "int8")
	return int8(n), err
}

// Int16 coerces to int16, via Int64.
func Int16(v any) (int16, error) {
	n, err := int64InRange(v, math.MinInt16, math.MaxInt16, "int16")
	return int16(n), err
}

// Int32 coerces to int32, via Int64.
func Int32(v any) (int32, error) {
	n, err := int64InRange(v, math.MinInt32, math.MaxInt32, "int32")
	return int32(n), err
}

func int64InRange(v any, min, max int64, name string) (int64, error) {
	n, err := Int64(v)
	if err != nil {
		return 0, err
	}
	if n < min || n > max {
		return 0, fmt.Errorf("conf: value overflows %s", name)
	}
	return n, nil
}

// Uint64 coerces a decoded file value to uint64, rejecting negatives.
func Uint64(v any) (uint64, error) {
	switch n := v.(type) {
	case uint:
		return uint64(n), nil
	case uint8:
		return uint64(n), nil
	case uint16:
		return uint64(n), nil
	case uint32:
		return uint64(n), nil
	case uint64:
		return n, nil
	case int, int8, int16, int32, int64:
		i, _ := Int64(v)
		if i < 0 {
			return 0, errors.New("conf: negative value for unsigned integer")
		}
		return uint64(i), nil
	case float64:
		if n != math.Trunc(n) || n < 0 || n >= math.MaxUint64 {
			return 0, errors.New("conf: value is not an integer that fits uint64")
		}
		return uint64(n), nil
	default:
		return 0, fmt.Errorf("conf: cannot use %T as integer", v)
	}
}

// Uint coerces to the platform uint, via Uint64.
func Uint(v any) (uint, error) {
	n, err := uint64InRange(v, math.MaxUint, "uint")
	return uint(n), err
}

// Uint8 coerces to uint8, via Uint64.
func Uint8(v any) (uint8, error) {
	n, err := uint64InRange(v, math.MaxUint8, "uint8")
	return uint8(n), err
}

// Uint16 coerces to uint16, via Uint64.
func Uint16(v any) (uint16, error) {
	n, err := uint64InRange(v, math.MaxUint16, "uint16")
	return uint16(n), err
}

// Uint32 coerces to uint32, via Uint64.
func Uint32(v any) (uint32, error) {
	n, err := uint64InRange(v, math.MaxUint32, "uint32")
	return uint32(n), err
}

func uint64InRange(v any, max uint64, name string) (uint64, error) {
	n, err := Uint64(v)
	if err != nil {
		return 0, err
	}
	if n > max {
		return 0, fmt.Errorf("conf: value overflows %s", name)
	}
	return n, nil
}

// Float64 coerces a decoded file value to float64: a float, or any integer
// a decoder produced for a number written without a decimal point.
func Float64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int, int8, int16, int32, int64:
		i, _ := Int64(v)
		return float64(i), nil
	case uint, uint8, uint16, uint32, uint64:
		u, _ := Uint64(v)
		return float64(u), nil
	default:
		return 0, fmt.Errorf("conf: cannot use %T as float", v)
	}
}

// Float32 coerces to float32, via Float64, rejecting magnitudes float32
// cannot hold rather than silently landing on +Inf.
func Float32(v any) (float32, error) {
	f, err := Float64(v)
	if err != nil {
		return 0, err
	}
	if math.Abs(f) > math.MaxFloat32 {
		return 0, errors.New("conf: value overflows float32")
	}
	return float32(f), nil
}
