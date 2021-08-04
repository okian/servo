package servo

import "context"

type Service interface {
	Name() string
	Initialize(context.Context) error
}

// returns all registered implementation of service if is there more than implementation for a service and add
// an prefix default implementation with examination (!) mark.
type Implementer interface {
	Implements() []string
}

type Finalizer interface {
	Finalize() error
}

type Readiness interface {
	Ready(context.Context) (interface{}, error)
}

type Healthiness interface {
	Healthy(context.Context) (interface{}, error)
}
