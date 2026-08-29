// Package mocks holds gomock-generated doubles for the three interfaces the
// service layer depends on. Regenerate with `go generate ./mocks/...` after
// changing any of the three source interfaces.
package mocks

//go:generate go run go.uber.org/mock/mockgen -source=../repository/repository.go -destination=repository_mock.go -package=mocks
//go:generate go run go.uber.org/mock/mockgen -source=../cache/cache.go -destination=cache_mock.go -package=mocks
//go:generate go run go.uber.org/mock/mockgen -source=../broker/broker.go -destination=broker_mock.go -package=mocks
