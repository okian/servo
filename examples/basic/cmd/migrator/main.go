package main

import (
	"context"
	"log"
	"time"
)

// shutdownTimeout bounds the whole teardown. servo.RunStop already caps each
// node at servo.DefaultStopBudget, but nothing caps their sum, and a container
// runtime sends SIGKILL when its grace period expires whether or not the
// process has finished unwinding.
const shutdownTimeout = 30 * time.Second

func main() {
	ctx := context.Background()

	app, err := New(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Run(ctx); err != nil {
		log.Print(err)
	}
	// Shutdown gets a fresh context: the one above is already cancelled — that
	// cancellation is what started the shutdown — so a teardown inheriting it
	// would abort before it began. It carries a deadline rather than being a
	// bare context.Background(), so the unwind cannot outlast the grace period
	// it is running inside.
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if r := app.Shutdown(sctx); !r.Clean() {
		log.Print(r)
	}
}
