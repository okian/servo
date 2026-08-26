module example.com/servobasic

go 1.25.0

require (
	github.com/okian/servo/v2 v2.0.0
	golang.org/x/sync v0.22.0
)

require (
	github.com/stretchr/testify v1.9.0 // indirect
	go.uber.org/goleak v1.3.0 // indirect
)

replace github.com/okian/servo/v2 => ../..
