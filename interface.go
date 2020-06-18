package servo

import "context"

type Service interface {
	Name() string
	Initialize(context.Context) error
	Finalize() error
	Healthy(context.Context) (interface{}, error)
	Ready(context.Context) (interface{}, error)
}
