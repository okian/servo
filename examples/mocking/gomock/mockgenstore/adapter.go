package mockgenstore

import (
	"go.uber.org/mock/gomock"

	"github.com/okian/servo/v2/servotest"
)

// MockStoreForServo wraps gomock-generated *MockStore, for the same
// wrapping reason as mockery (NewMockStore(ctrl) is a valid, colliding
// provider shape). *gomock.Controller itself needs a TestReporter, and
// there's no *testing.T reachable from inside the graph — servotest's
// PanicReporter supplies one without pulling gomock into servotest itself.
// The constructor stays zero-arg (a real *testing.T can't be a constructor
// parameter servo would know how to satisfy) — that's the whole reason
// PanicReporter exists.
type MockStoreForServo struct {
	*MockStore
	Finish func()
}

func NewMockStoreForServo() *MockStoreForServo {
	ctrl := gomock.NewController(servotest.PanicReporter{})
	return &MockStoreForServo{MockStore: NewMockStore(ctrl), Finish: ctrl.Finish}
}
