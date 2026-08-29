package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/servoscoped/chat"
)

// shutdownTimeout bounds the whole teardown. servo.RunStop already caps each
// node at servo.DefaultStopBudget, but nothing caps their sum, and a container
// runtime sends SIGKILL when its grace period expires whether or not the
// process has finished unwinding.
const shutdownTimeout = 30 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := New(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Two callers presenting the same key share one room; the third gets
	// its own. In a real service this context comes from middleware at the
	// transport edge, not from main.
	if err := app.server.Post(chat.WithRoom(ctx, "general"), "hello"); err != nil {
		log.Print(err)
	}
	if err := app.server.Post(chat.WithRoom(ctx, "general"), "still here"); err != nil {
		log.Print(err)
	}
	if err := app.server.Post(chat.WithRoom(ctx, "random"), "elsewhere"); err != nil {
		log.Print(err)
	}
	log.Printf("rooms: %+v", app.rooms.Stats())

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
