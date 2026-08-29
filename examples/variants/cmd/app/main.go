// Command app builds one of two graphs depending on the build tags.
//
//	go build ./...              -> wired against memory.Mem
//	go build -tags=prod ./...   -> wired against postgres.DB
//
// main.go itself is identical in both: New comes from whichever generated
// file the build selects, and nothing here mentions a tag.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
