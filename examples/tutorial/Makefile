.PHONY: up down test test-integration run run-gin run-grpc generate check

up:
	docker compose -f deploy/docker-compose.yml up -d

down:
	docker compose -f deploy/docker-compose.yml down -v

test:
	go test ./...

# Requires `make up` first — these hit real Postgres/Redis/NATS rather than
# mocks, and are skipped automatically (not failed) if the env vars below
# aren't set.
test-integration:
	TEST_POSTGRES_DSN="postgres://orders:orders@localhost:5432/orders?sslmode=disable" \
	TEST_REDIS_ADDR="localhost:6379" \
	TEST_NATS_URL="nats://localhost:4222" \
	go test ./... -v

# Four transports over the same service layer — see
# docs/tutorial/10-api-layer.md, 11-gin-transport.md, 12-grpc-transport.md
# and 13-directive-transport.md. Each is its own injector, so `generate`
# and `check` below cover all four at once.
run:
	go run ./cmd/orders

run-gin:
	go run ./cmd/ordersgin

# Serves gRPC and REST on the same port.
run-grpc:
	go run ./cmd/ordersgrpc

# The //servo: directive transport: no hand-written server at all.
run-servo:
	go run ./cmd/ordersservo

# `go tool servo` runs the generator go.mod pins with its tool directive
# (`go get -tool github.com/okian/servo/v3/cmd/servo` adds it), so every
# machine generates with the same version. `go run <the generator's path>`
# would not work outside the servo repository: a module requires servo for
# its marker package alone, so the generator's own dependencies are not
# in its build list.
generate:
	go tool servo generate

check:
	go tool servo check
