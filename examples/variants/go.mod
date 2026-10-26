module example.com/servovariants

go 1.26.0

require github.com/okian/servo/v3 v3.0.0

require (
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
)

replace github.com/okian/servo/v3 => ../..

tool github.com/okian/servo/v3/cmd/servo
