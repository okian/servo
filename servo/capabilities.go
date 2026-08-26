package servo

import "context"

// Capability interfaces are detected structurally by the generator via
// types.Implements. Components implement these by shape alone and never
// import servo to do so.
type (
	Initializer interface {
		Init(ctx context.Context) error
	}
	Runner interface {
		Run(ctx context.Context) error
	}
	Drainer interface {
		Drain(ctx context.Context) error
	}
	Flusher interface {
		Flush(ctx context.Context) error
	}
	Finalizer interface {
		Stop(ctx context.Context) error
	}
	Healther interface {
		Health(ctx context.Context) error
	}
	Readier interface {
		Ready(ctx context.Context) error
	}
)
