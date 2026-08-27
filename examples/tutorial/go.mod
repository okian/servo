module example.com/servoorders

go 1.25.0

require (
	github.com/caarlos0/env/v11 v11.4.1
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/nats-io/nats.go v1.53.1
	github.com/okian/servo/v3 v3.0.0-00010101000000-000000000000
	github.com/redis/go-redis/v9 v9.22.0
	go.uber.org/mock v0.6.0
	golang.org/x/crypto v0.49.0
	golang.org/x/sync v0.22.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/goleak v1.3.0 // indirect
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.35.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
)

replace github.com/okian/servo/v3 => ../..

tool go.uber.org/mock/mockgen
