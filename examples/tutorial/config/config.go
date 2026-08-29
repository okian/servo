// Package config supplies configuration values without knowing what any
// of them are.
//
// It deliberately declares no settings of its own. Each package owns the
// fields it needs, on its own Config type, and fills them by asking a
// Source — so adding or removing a setting is a change to exactly one
// package, and this one never grows a field list that every component
// must agree on.
package config

import (
	"os"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Source is where configuration values come from.
//
// It is an interface, and it deliberately says nothing about which
// settings exist: a component declares the fields it needs on its own
// Config type and asks a Source to fill them, so adding or removing a
// setting touches only the package that owns it. This package never
// learns what those fields are.
//
// It is also why no component calls os.Getenv itself. A package that
// reads the environment directly assumes there is one, which makes it
// awkward to test and impossible to feed from anywhere else. Taking a
// Source as an ordinary constructor parameter leaves both open.
type Source interface {
	// Values returns every key the source knows about. Callers must not
	// mutate the result.
	Values() map[string]string
}

// Env is the Source backed by the process environment. It is read once,
// at construction, so nothing built later can observe it changing.
type Env struct{ values map[string]string }

var _ Source = (*Env)(nil)

func NewEnv() *Env {
	environ := os.Environ()
	values := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			values[k] = v
		}
	}
	return &Env{values: values}
}

func (e *Env) Values() map[string]string { return e.values }

// Parse fills a package's own config type from src. It is generic so that
// the type stays in the package that declares it: this function never
// names a field, and never needs changing when one is added.
//
// prefix namespaces the keys, which is what lets two packages each
// declare a field called URL without agreeing on anything — natsbroker
// asks for "NATS_", redis for "REDIS_". A missing value is reported under
// the full prefixed name, so the error names the variable an operator has
// to set. Pass "" for settings that are genuinely app-wide.
//
//

// Parse fills a package's own config type from src. It is generic so that
// the type stays in the package that declares it: this function never
// names a field, and never needs changing when one is added.
//
// prefix namespaces the keys, which is what lets two packages each
// declare a field called URL without agreeing on anything — natsbroker
// asks for "NATS_", redis for "REDIS_". A missing value is reported under
// the full prefixed name, so the error names the variable an operator has
// to set. Pass "" for settings that are genuinely app-wide.
//
//	type Config struct {
//	    URL string `env:"URL,required"`
//	}
//
//	func NewConfig(src config.Source) (*Config, error) {
//	    return config.Parse[Config](src, "NATS_")
//	}
func Parse[T any](src Source, prefix string) (*T, error) {
	cfg, err := env.ParseAsWithOptions[T](env.Options{
		Environment: src.Values(),
		Prefix:      prefix,
	})
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}
