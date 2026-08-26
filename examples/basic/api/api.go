package api

import (
	"context"
	"log"
	"time"

	"example.com/servobasic/store"
)

type Server struct {
	store store.Store
}

func New(st store.Store) *Server {
	return &Server{store: st}
}

func (s *Server) Ready(ctx context.Context) error {
	return nil
}

// Lookup is the kind of real request-handling code a test exercises
// through the mock: call it, then assert on what the mock recorded.
func (s *Server) Lookup(key string) string {
	return s.store.Get(key)
}

func (s *Server) Run(ctx context.Context) error {
	log.Println("api: serving (Ctrl+C to stop)")
	<-ctx.Done()
	log.Println("api: run loop exiting")
	return nil
}

func (s *Server) Drain(ctx context.Context) error {
	log.Println("api: draining in-flight requests")
	time.Sleep(50 * time.Millisecond)
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	log.Println("api: stopped")
	return nil
}
