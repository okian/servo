package main

import (
	"context"
	"flag"

	"example.com/servobasic/migrator"
	"github.com/okian/servo/v3/servo"
)

func main() {
	target := flag.String("target", "latest", "schema version to migrate to")
	flag.Parse()

	// NewWith, not New: the target only exists once flag.Parse has run, so
	// it is declared with servo.Value and handed in here. New still exists
	// and would compile, but it can only pass the zero value — which for a
	// schema version is the empty string, not "latest".
	//
	// The migration runs in Init, so a failed one fails startup: servo.Main
	// logs it and exits 1, which is what a deploy step waiting on this
	// binary needs to see.
	servo.Main(func(ctx context.Context) (*App, error) {
		return NewWith(ctx, Values{Target: migrator.Target(*target)})
	})
}
