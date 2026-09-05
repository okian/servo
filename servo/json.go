package servo

// Json is the response wrapper a //servo: handler returns:
//
//	func Order(ctx context.Context, req *OrderReq) (servo.Json[*OrderResp], error)
//
// It is an interface rather than a struct so the error path can return a
// plain nil, and it is sealed (the unexported method below) so servo.JSON
// stays the only way to construct one — the generated adapter can then
// treat every non-nil value uniformly.
type Json[T any] interface {
	// Value returns the payload the generated adapter encodes as the
	// response body. Exported because the adapter lives in the user's own
	// package, not in servo.
	Value() T

	sealedJSON()
}

// JSON wraps a handler's payload for encoding as application/json. The
// constructor is spelled JSON rather than Json because Go permits one
// identifier per name per package, and the type owns Json.
func JSON[T any](v T) Json[T] {
	return jsonValue[T]{v: v}
}

type jsonValue[T any] struct{ v T }

func (j jsonValue[T]) Value() T    { return j.v }
func (j jsonValue[T]) sealedJSON() {}
