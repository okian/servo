package mockstore

// NewStoreMock exists because moq generates a plain struct with function
// fields and no constructor at all — servo resolves a graph by calling a
// provider function it found, never by evaluating a composite literal, so
// StoreMock needs one. A separate, hand-written file: regenerating
// store_mock.go would erase anything added to it directly.
func NewStoreMock() *StoreMock { return &StoreMock{} }
