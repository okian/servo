package grpcapi

// The generated code in ordersv1/ is committed, exactly as the gomock
// mocks are: a reader cloning this repository can build and test it
// without installing protoc and two plugins first. Regenerate after
// editing the .proto with:
//
//	go generate ./grpcapi/...
//
// The gofumpt pass is part of generating, not a manual step afterwards.
// protoc-gen-go emits one alphabetical import block mixing the standard
// library with module paths, which gofmt accepts and every import
// organizer disagrees with — so a checked-in generated file would be
// reported unformatted by any tool pointed at it, including this
// repository's pre-commit hook. Running it here means the committed file
// and a freshly generated one are the same file.
//
// The two files are named individually because gofumpt skips anything
// carrying a "DO NOT EDIT" header when it walks a directory, and both of
// these have one. Passing a path explicitly formats it anyway, which is
// also how the pre-commit hook sees them: as staged paths, not as a tree.
//
//go:generate protoc --proto_path=ordersv1 --go_out=ordersv1 --go_opt=paths=source_relative --go-grpc_out=ordersv1 --go-grpc_opt=paths=source_relative ordersv1/orders.proto
//go:generate gofumpt -w ordersv1/orders.pb.go ordersv1/orders_grpc.pb.go
