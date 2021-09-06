package vol

import (
	"context"
	"errors"
	"io"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/log"
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
	f := trace(ctx, path)
	r, err := impl.Load(ctx, path)
	return r, f(err)
}
func Save(ctx context.Context, path string, r io.Reader) error {
	return trace(ctx, path)(impl.Save(ctx, path, r))
}
func Delete(ctx context.Context, path string) error {
	return trace(ctx, path)(impl.Delete(ctx, path))
}
func Exist(ctx context.Context, path string) (bool, error) {
	f := trace(ctx, path)
	b, err := impl.Exist(ctx, path)
	return b, f(err)
}

func trace(ctx context.Context, p string) func(err error) error {
	sp := opentracing.SpanFromContext(ctx)
	if sp == nil {
		return func(err error) error {
			return err
		}
	}
	ch := opentracing.StartSpan("vol", opentracing.ChildOf(sp.Context()))
	logs := []log.Field{log.String("path", p)}
	return func(e error) error {
		if e != nil {
			logs = append(logs, log.Error(e))
			ch.SetTag("error", true)
		}
		ch.LogFields(logs...)
		ch.Finish()
		return e
	}
}
