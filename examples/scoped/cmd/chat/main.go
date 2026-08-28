package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"example.com/servoscoped/chat"
)

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

	if r := app.Shutdown(context.Background()); !r.Clean() {
		log.Print(r)
	}
}
