// Package api is identical in both configurations: it depends on the
// store.Store interface and never learns which implementation it got.
package api

import (
	"context"
	"fmt"

	"example.com/servovariants/store"
)

type Server struct{ store store.Store }

func New(s store.Store) *Server { return &Server{store: s} }

// Init makes Server an Initializer, so servo calls it during startup.
func (s *Server) Init(ctx context.Context) error {
	greeting, err := s.store.Get(ctx, "greeting")
	if err != nil {
		return err
	}
	fmt.Println(greeting)
	return nil
}

// Run makes Server a Runner, so servo supervises it until the context is
// cancelled.
func (s *Server) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
