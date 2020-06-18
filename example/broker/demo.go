package broker

import (
	"context"

	"github.com/okian/servo"
)

type Broker struct {
}

func (b Broker) Name() string {
	// it MUST be unique otherwise Register will return error
	return "msg broker"
}

func (b Broker) Initialize(ctx context.Context) error {
	// setup broker and connect to server and if every when well return nil
	return nil
}

func (b Broker) Finalize() error {
	// cleanup and close connection
	return nil
}

func (b Broker) Healthy(ctx context.Context) (interface{}, error) {
	// check your connection and status first return value is optional report of
	// your service
	return struct {
		Status                            string
		AnswerToTheUltimateQuestionOfLife int
	}{
		Status:                            "OK",
		AnswerToTheUltimateQuestionOfLife: 42,
	}, nil
}

func (b Broker) Ready(ctx context.Context) (interface{}, error) {
	// Are you ready to serve? if so return optional report and nil for error
	return nil, nil
}

func init() {
	// order is for when you have services that depend on each other
	// servo will initialize services from smallest order to the biggest and
	// finalize will do it in opposite order
	// services with same order number will initiate concurrently
	if err := servo.Register(Broker{}, 20); err != nil {
		// you will get error when you have more then one service with same name
		panic(err)
	}
}
