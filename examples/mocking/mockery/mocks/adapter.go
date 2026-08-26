package mocks

// StoreMock embeds mockery's generated *Store — a distinct result type,
// so it never collides with Store's own constructor (NewStore, which
// takes a *testing.T-shaped value and is itself a valid provider shape
// servo could call, making a second, zero-arg constructor for the same
// *Store type a genuine ambiguity). A separate, hand-written file: this
// package is otherwise regenerated wholesale by mockery.
type StoreMock struct{ *Store }

func NewStoreMock() *StoreMock { return &StoreMock{Store: &Store{}} }
