package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/servoscoped/chat"
	"github.com/okian/servo/v3/servo"
)

// shutdownTimeout bounds the whole teardown. servo.DefaultStopBudget caps
// each node, but nothing caps their sum, and a container runtime sends
// SIGKILL when its grace period runs out whether or not the process has
// finished unwinding: 25 seconds stays inside the 30 Kubernetes allows by
// default.
const shutdownTimeout = 25 * time.Second

// main drives the App by hand — a few posts, then a look at the scope —
// instead of running it until a signal, so it is the one example that does
// not hand itself to servo.Main. It ends the way servo.Main does: Shutdown
// under a deadline of its own, then servo.ExitCode, so a failed post or an
// unclean Shutdown exits 1.
func main() { os.Exit(run()) }

func run() int {
	// A scoped instance needs a context that can end — Acquire refuses
	// context.Background — so the posts run under the signal's.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := New(ctx)
	if err != nil {
		log.Print(err)
		return 1
	}

	// Two callers presenting the same key share one room; the third gets
	// its own. In a real service this context comes from middleware at the
	// transport edge, not from main.
	err = errors.Join(
		app.server.Post(chat.WithRoom(ctx, "general"), "hello"),
		app.server.Post(chat.WithRoom(ctx, "general"), "still here"),
		app.server.Post(chat.WithRoom(ctx, "random"), "elsewhere"),
	)
	if err != nil {
		log.Print(err)
	}
	log.Printf("rooms: %+v", app.rooms.Stats())

	// A fresh context with a deadline, not ctx: a signal cancels ctx, and a
	// teardown inheriting that would abandon every node before it began.
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	r := app.Shutdown(sctx)
	if !r.Clean() {
		log.Print(r)
	}
	return servo.ExitCode(err, r)
}
