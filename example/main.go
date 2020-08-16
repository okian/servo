package main

import (
	"context"

	"github.com/okian/servo"
	// DONT FORGET THIS (needed for invoking init function)
	_ "github.com/okian/servo/example/broker"
)

func main() {
	ctx, cl := context.WithCancel(context.Background())
	defer cl()
	defer servo.Initialize(ctx)()

	// do your things
	// ...

}
