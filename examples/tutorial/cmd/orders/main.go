package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/servoorders/internal/transport/admin"
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

	// Health, readiness and metrics go on their own listener, never the
	// public one — see package admin. app.Health and app.Ready are only
	// reachable once App is fully constructed, so no component inside the
	// graph could serve them: this one piece of wiring belongs here.
	//
	// The address comes off the graph rather than being parsed a second
	// time: servo already built an *api.Config, and this file is in the
	// same package as the generated code.
	adminSrv := admin.New(app.apiConfig.AdminAddr, app, app.server.MetricsHandler())
	go func() {
		if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Print(err)
		}
	}()

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
	adminSrv.Shutdown(sctx)
	if r := app.Shutdown(sctx); !r.Clean() {
		log.Print(r)
	}
}
