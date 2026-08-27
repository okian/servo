module example.com/servoorders

go 1.25.0

require (
	github.com/caarlos0/env/v11 v11.4.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace github.com/okian/servo/v3 => ../..
