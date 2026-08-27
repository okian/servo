package mocks

import (
	"go.uber.org/mock/gomock"

	"github.com/okian/servo/v3/servotest"
)

// The four wrappers below exist for the same reason servo's own
// examples/mocking/gomock needs one: NewMockX(ctrl) is a valid provider
// shape servo could call, but *gomock.Controller itself needs a
// TestReporter, and there's no *testing.T reachable from inside a
// generated graph. servotest.PanicReporter supplies one without servotest
// (or this package) importing gomock. The constructors stay zero-arg,
// since a real *testing.T can't be a constructor parameter servo would
// know how to satisfy — that's the whole reason PanicReporter exists. See
// docs/tutorial/11-wiring-with-servo.md.

type OrderRepositoryForServo struct {
	*MockOrderRepository
	Finish func()
}

func NewOrderRepositoryForServo() *OrderRepositoryForServo {
	ctrl := gomock.NewController(servotest.PanicReporter{})
	return &OrderRepositoryForServo{MockOrderRepository: NewMockOrderRepository(ctrl), Finish: ctrl.Finish}
}

type UserRepositoryForServo struct {
	*MockUserRepository
	Finish func()
}

func NewUserRepositoryForServo() *UserRepositoryForServo {
	ctrl := gomock.NewController(servotest.PanicReporter{})
	return &UserRepositoryForServo{MockUserRepository: NewMockUserRepository(ctrl), Finish: ctrl.Finish}
}

type OrderCacheForServo struct {
	*MockOrderCache
	Finish func()
}

func NewOrderCacheForServo() *OrderCacheForServo {
	ctrl := gomock.NewController(servotest.PanicReporter{})
	return &OrderCacheForServo{MockOrderCache: NewMockOrderCache(ctrl), Finish: ctrl.Finish}
}

type EventPublisherForServo struct {
	*MockEventPublisher
	Finish func()
}

func NewEventPublisherForServo() *EventPublisherForServo {
	ctrl := gomock.NewController(servotest.PanicReporter{})
	return &EventPublisherForServo{MockEventPublisher: NewMockEventPublisher(ctrl), Finish: ctrl.Finish}
}
