package mockgenstore

//go:generate go run go.uber.org/mock/mockgen -source=../../store/store.go -destination=store_mock.go -package=mockgenstore
