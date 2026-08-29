package main

import (
	"context"
	"flag"
	"log"
	"time"

	"example.com/servobasic/migrator"
)

// shutdownTimeout bounds the whole teardown. servo.RunStop already caps each
// node at servo.DefaultStopBudget, but nothing caps their sum, and a container
// runtime sends SIGKILL when its grace period expires whether or not the
// process has finished unwinding.
const shutdownTimeout = 30 * time.Second

func main() {
	target := flag.String("target", "latest", "schema version to migrate to")
	flag.Parse()

	ctx := context.Background()

	// NewWith, not New: the target only exists once flag.Parse has run, so
	// it is declared with servo.Value and handed in here. New still exists
	// and would compile, but it can only pass the zero value — which for a
	// schema version is the empty string, not "latest".
	app, err := NewWith(ctx, Values{Target: migrator.Target(*target)})
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
