package servo

import (
	"context"
	"sync"
	"testing"
	"time"
)

func cleanup() {
	register = make(map[int][]Service)
	registerLock = sync.RWMutex{}
	initialized = false
	finalized = false
	flatOnce = sync.Once{}
	services = nil
	serviceNames = make(map[string]bool)
}

type testService struct {
	name         string
	initDelay    time.Duration
	initError    error
	healthDelay  time.Duration
	healthError  error
	healthResult interface{}
	readyDelay   time.Duration
	readyResult  interface{}
	readyError   error
	test         *testing.T
}

func (t testService) Name() string {
	return t.name
}

func (t testService) Initialize(ctx context.Context) error {
	select {
	case <-time.After(t.initDelay):
		t.test.Logf("initialize: %q", t.name)
		return t.initError
	case <-ctx.Done():
		return nil
	}
}

func (t testService) Finalize() error {
	t.test.Logf("finalize: %q", t.name)
	<-time.After(t.initDelay)
	return t.initError
}

func (t testService) Healthy(ctx context.Context) (interface{}, error) {
	select {
	case <-time.After(t.initDelay):
		t.test.Logf("health: %q", t.name)
		return t.healthResult, t.healthError
	case <-ctx.Done():
		return nil, nil
	}
}

func (t testService) Ready(ctx context.Context) (interface{}, error) {
	select {
	case <-time.After(t.initDelay):
		t.test.Logf("ready: %q", t.name)
		return t.readyResult, t.readyError
	case <-ctx.Done():
		return nil, nil
	}
}

func TestRegister(t *testing.T) {

	delay := time.Duration(0)
	err := Register(&testService{
		test:      t,
		name:      "first",
		initDelay: delay,
	}, 1)
	if err != nil {
		t.Error(err)
	}
	err = Register(testService{
		test:      t,
		name:      "second",
		initDelay: delay,
	}, 2)
	if err != nil {
		t.Error(err)
	}
	err = Register(testService{
		test:      t,
		name:      "third",
		initDelay: delay,
	}, 3)
	if err != nil {
		t.Error(err)
	}
	err = Register(testService{
		test:      t,
		name:      "forth 1",
		initDelay: delay,
	}, 4)
	if err != nil {
		t.Error(err)
	}
	err = Register(testService{
		test:      t,
		name:      "forth 2",
		initDelay: delay,
	}, 4)
	if err != nil {
		t.Error(err)
	}
	err = Register(testService{
		test:      t,
		name:      "forth 2",
		initDelay: delay,
	}, 4)
	if err == nil {
		t.Error(err)
	}
	err = nil
	ctx, cl := context.WithTimeout(context.Background(), time.Second*60)
	defer cl()
	err = Initialize(ctx)
	if err != nil {
		t.Error(err)
	}
	_, err = Health(ctx)
	if err != nil {
		t.Error(err)
	}
	_, err = Ready(ctx)
	if err != nil {
		t.Error(err)
	}
	err = Finalize(ctx)
	if err != nil {
		t.Error(err)
	}

}
