package config

import (
	"strings"
	"testing"
	"time"
)

// static is a Source with values supplied directly — the reason Source is
// an interface at all. A test that wants a particular configuration says
// so, rather than arranging the process environment to produce it.
type static map[string]string

func (s static) Values() map[string]string { return s }

// probe stands in for any package's own config type. This package never
// sees a real one, which is the point: Parse names no field.
type probe struct {
	Addr     string        `env:"ADDR,required"`
	Retries  int           `env:"RETRIES"`
	Enabled  bool          `env:"ENABLED"`
	Timeout  time.Duration `env:"TIMEOUT"`
	Rate     float64       `env:"RATE"`
	Subjects []string      `env:"SUBJECTS" envSeparator:","`
	Fallback int           `env:"FALLBACK" envDefault:"7"`
}

func TestParseConvertsTypedFieldsFromAStringSource(t *testing.T) {
	cfg, err := Parse[probe](static{
		"P_ADDR": "localhost:1", "P_RETRIES": "3", "P_ENABLED": "true",
		"P_TIMEOUT": "90s", "P_RATE": "1.5", "P_SUBJECTS": "a,b,c",
	}, "P_")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	switch {
	case cfg.Addr != "localhost:1":
		t.Errorf("Addr = %q", cfg.Addr)
	case cfg.Retries != 3:
		t.Errorf("Retries = %d", cfg.Retries)
	case !cfg.Enabled:
		t.Error("Enabled = false")
	case cfg.Timeout != 90*time.Second:
		t.Errorf("Timeout = %v", cfg.Timeout)
	case cfg.Rate != 1.5:
		t.Errorf("Rate = %v", cfg.Rate)
	case len(cfg.Subjects) != 3:
		t.Errorf("Subjects = %v", cfg.Subjects)
	case cfg.Fallback != 7:
		t.Errorf("Fallback = %d, want the envDefault", cfg.Fallback)
	}
}

// The prefix is what lets two packages each declare a field called Addr
// without coordinating: they pass different prefixes and never collide.
func TestPrefixKeepsTwoPackagesApart(t *testing.T) {
	type other struct {
		Addr string `env:"ADDR,required"`
	}
	src := static{"ONE_ADDR": "first", "TWO_ADDR": "second"}

	a, err := Parse[probe](src, "ONE_")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, err := Parse[other](src, "TWO_")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if a.Addr != "first" || b.Addr != "second" {
		t.Errorf("prefixes did not keep them apart: %q / %q", a.Addr, b.Addr)
	}
}

// A missing value has to name the variable an operator must set, which is
// the prefixed one — not the bare field tag.
func TestMissingRequiredValueNamesThePrefixedVariable(t *testing.T) {
	_, err := Parse[probe](static{"ADDR": "unprefixed, so not found"}, "P_")
	if err == nil {
		t.Fatal("expected an error for the missing P_ADDR")
	}
	if got := err.Error(); !strings.Contains(got, "P_ADDR") {
		t.Errorf("error = %q, want it to name P_ADDR", got)
	}
}

// An injected Source must be the whole truth: if the process environment
// leaked in, a test could pass because of the machine it ran on.
func TestAnInjectedSourceDoesNotFallBackToTheProcessEnvironment(t *testing.T) {
	t.Setenv("P_ADDR", "from-the-process")
	if _, err := Parse[probe](static{}, "P_"); err == nil {
		t.Error("Parse saw P_ADDR from the process environment; the Source should be the only input")
	}
}

func TestEnvReadsTheProcessEnvironmentOnce(t *testing.T) {
	t.Setenv("SNAPSHOT_KEY", "before")
	src := NewEnv()
	t.Setenv("SNAPSHOT_KEY", "after")

	if got := src.Values()["SNAPSHOT_KEY"]; got != "before" {
		t.Errorf("Values()[SNAPSHOT_KEY] = %q, want the value read at construction", got)
	}
}
