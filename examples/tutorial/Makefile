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

# Three transports over the same service layer — see
# docs/tutorial/19-transport-choices.md. Each is its own injector, so
# `generate` and `check` below cover all three at once.
run:
	go run ./cmd/orders

run-gin:
	go run ./cmd/ordersgin

# Serves gRPC and REST on the same port.
run-grpc:
	go run ./cmd/ordersgrpc

generate:
	go run github.com/okian/servo/v3/cmd/servo generate

check:
	go run github.com/okian/servo/v3/cmd/servo check
