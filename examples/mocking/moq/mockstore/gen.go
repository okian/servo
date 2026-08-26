package mockstore

//go:generate go run github.com/matryer/moq -pkg mockstore -out store_mock.go ../../store Store:StoreMock
