package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const shutdownTimeout = 30 * time.Second

// main is the whole binary: the listener, the routes, the middleware chain
// and the admin story all live in the graph or the generated file now. The
// admin listener the api transport wires by hand in cmd/orders could be
// added here the same way; the chapter keeps this main minimal so the
// difference stays visible.
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

	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if r := app.Shutdown(sctx); !r.Clean() {
		log.Print(r)
	}
}
