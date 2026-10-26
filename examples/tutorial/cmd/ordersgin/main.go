package main

import (
	"context"

	"github.com/okian/servo/v3/servo"

	"example.com/servoorders/internal/transport/admin"
)

// main hands the App to servo.Main, which runs it until SIGINT, SIGTERM or
// SIGHUP, shuts it down under a deadline, logs any failure at ERROR and
// exits 1 for it.
//
// The probes and metrics go on their own listener, never the public one —
// see package admin. app.Health and app.Ready only exist once New has
// built the App, so no component inside the graph could serve them: this
// one piece of wiring belongs here. The address comes off the server
// rather than being parsed a second time: the loaded config is a local
// inside the generated New, so the one component that received it answers
// for it.
func main() {
	servo.Main(func(ctx context.Context) (*admin.Server, error) {
		app, err := New(ctx)
		if err != nil {
			return nil, err
		}
		return admin.Mount(app, app.server.AdminAddr(), app.server.MetricsHandler()), nil
	})
}
