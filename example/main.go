package main

import (
	"context"
	"time"

	"github.com/okian/servo"
	// DONT FORGET THIS (needed for invoking init function)
	_ "github.com/okian/servo/example/broker"
)

func main() {
	ctx, cl := context.WithTimeout(context.Background(), time.Minute)
	defer cl()
	err := servo.Initialize(ctx)
	if err != nil {
		panic(err)
	}

	// do your things
	// ...

	err = servo.Finalize(ctx)
	if err != nil {
		panic(err)
	}
}
