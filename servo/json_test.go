package servo

import (
	"errors"
	"testing"
)

type orderResp struct {
	ID    string
	Items int
}

func TestJSONValueRoundTrip(t *testing.T) {
	v := &orderResp{ID: "o-1", Items: 3}
	j := JSON(v)
	if got := j.Value(); got != v {
		t.Fatalf("JSON(v).Value() = %p, want the same pointer %p", got, v)
	}
}

func TestJSONValueKinds(t *testing.T) {
	t.Run("nil pointer payload", func(t *testing.T) {
		j := JSON[*orderResp](nil)
		if got := j.Value(); got != nil {
			t.Fatalf("Value() = %v, want nil", got)
		}
	})

	t.Run("value payload", func(t *testing.T) {
		j := JSON(orderResp{ID: "o-2"})
		if got := j.Value(); got.ID != "o-2" {
			t.Fatalf("Value() = %+v, want ID o-2", got)
		}
	})

	t.Run("slice payload", func(t *testing.T) {
		j := JSON([]int{1, 2, 3})
		if got := j.Value(); len(got) != 3 {
			t.Fatalf("Value() = %v, want 3 elements", got)
		}
	})
}

// A handler's error path returns a plain nil in the Json position:
//
//	return nil, servo.Status.NOT_FOUND.Wrapf(...)
//
// which only compiles if Json is an interface. This test pins that shape by
// exercising a handler-shaped function: turning Json into a struct would
// break the nil return below before it broke any user.
func TestJsonNilOnErrorPath(t *testing.T) {
	handler := func(fail bool) (Json[*orderResp], error) {
		if fail {
			return nil, errors.New("nope")
		}
		return JSON(&orderResp{ID: "ok"}), nil
	}

	j, err := handler(true)
	if err == nil {
		t.Fatalf("expected the error path")
	}
	if j != nil {
		t.Fatalf("error path must return a nil Json, got %v", j)
	}
}
