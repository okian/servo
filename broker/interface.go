package broker

import (
	"context"
	"errors"
)

type Message interface {
	Payload() []byte
	Commit() error
}

type Interface interface {
	Publish(ctx context.Context, topic string, msg []byte) (string, error)
	Consume(ctx context.Context, topic string) <-chan Message
}

var impl Interface

func Register(i Interface) error {
	if impl != nil {
		return errors.New("broker: Register driver is nil")
	}
	impl = i
	return nil
}

func Publish(ctx context.Context, topic string, msg []byte) (string, error) {
	return impl.Publish(ctx, topic, msg)
}

func Consume(ctx context.Context, topic string) <-chan Message {
	return impl.Consume(ctx, topic)
}
