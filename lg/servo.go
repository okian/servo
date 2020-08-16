package lg

import (
	"context"
)

type in struct {
	loggers []string
}

func (i *in) Name() string {
	return "log"
}

func (i *in) Initialize(ctx context.Context) error {
	return nil
}

func (i *in) Finalize() error {
	return nil
}
func mapToSlice() []string {
	lock.RLock()
	defer lock.RUnlock()
	r := make([]string, 0)
	for k := range loggers {
		r = append(r, k)
	}
	return r
}

func (i *in) Healthy(_ context.Context) (interface{}, error) {
	if i.loggers == nil {
		i.loggers = mapToSlice()
	}
	return i.loggers, nil
}

func (i *in) Ready(_ context.Context) (interface{}, error) {
	if i.loggers == nil {
		i.loggers = mapToSlice()
	}
	return i.loggers, nil
}
