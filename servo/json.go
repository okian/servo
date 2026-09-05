package servo

// Json is the response wrapper a JSON //servo: handler returns:
//
//	func Order(ctx context.Context, req *OrderReq) (servo.Json[*OrderResp], error)
//
// It is one member of the sealed Response family (see response.go): the
// typed form keeps the response schema visible in the signature, while a
// handler choosing its encoding at runtime declares servo.Response instead.
// Being an interface is what lets the error path return a plain nil.
type Json[T any] interface {
	Response
	// Value returns the payload, mostly for tests — encoding happens
	// through WriteResponse.
	Value() T
}

// JSON wraps a handler's payload for encoding as application/json. The
// constructor is spelled JSON rather than Json because Go permits one
// identifier per name per package, and the type owns Json.
func JSON[T any](v T) Json[T] {
	return jsonValue[T]{v: v}
}

type jsonValue[T any] struct{ v T }

func (j jsonValue[T]) Value() T { return j.v }
