.PHONY: up down test test-integration run generate check

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

run:
	go run ./cmd/orders

generate:
	go run github.com/okian/servo/v3/cmd/servo generate

check:
	go run github.com/okian/servo/v3/cmd/servo check
