package vol

import (
	"context"
	"errors"
	"io"
)

var NotFound = errors.New("not found")
var impl Interface

func Register(v Interface) {
	impl = v
}

type Interface interface {
	Load(context.Context, string) (io.ReadCloser, error)
	Save(context.Context, string, io.Reader) error
	Delete(context.Context, string) error
	Exist(context.Context, string) (bool, error)
}

func Load(ctx context.Context, path string) (io.ReadCloser, error) {
	return impl.Load(ctx, path)
}
func Save(ctx context.Context, path string, r io.Reader) error {
	return impl.Save(ctx, path, r)
}
func Delete(ctx context.Context, path string) error {
	return impl.Delete(ctx, path)
}
func Exist(ctx context.Context, path string) (bool, error) {
	return impl.Exist(ctx, path)
}
