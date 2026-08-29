package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"example.com/servoorders/admin"
)

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
	adminSrv := admin.New(app.grpcapiConfig.AdminAddr, app, app.server.MetricsHandler())
	go func() {
		if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Print(err)
		}
	}()

	if err := app.Run(ctx); err != nil {
		log.Print(err)
	}

	adminSrv.Shutdown(context.Background())
	if r := app.Shutdown(context.Background()); !r.Clean() {
		log.Print(r)
	}
}
