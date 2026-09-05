module example.com/servohttp

go 1.27.0

require (
	github.com/okian/servo/v3 v3.0.0
	golang.org/x/sync v0.22.0
)

replace github.com/okian/servo/v3 => ../..
