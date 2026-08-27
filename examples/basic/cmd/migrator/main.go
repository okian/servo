package main

import (
	"context"
	"log"
)

func main() {
	ctx := context.Background()

	app, err := New(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Run(ctx); err != nil {
		log.Print(err)
	}
	if r := app.Shutdown(context.Background()); !r.Clean() {
		log.Print(r)
	}
}
