//go:build !prod

// Package memory is the default build's store. It exists only when prod is
// unset, which is what makes it a build variant rather than a runtime
// switch: under -tags=prod this file is not compiled and the type does not
// exist at all.
package memory

import "context"

type Mem struct{ data map[string]string }

func New() *Mem { return &Mem{data: map[string]string{"greeting": "hello from memory"}} }

func (m *Mem) Get(ctx context.Context, key string) (string, error) { return m.data[key], nil }
