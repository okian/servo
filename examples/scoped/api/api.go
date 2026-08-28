// Package api is an ordinary singleton that consumes a scope. It depends
// on chat.Rooms — the accessor interface — never on *chat.Room directly:
// a singleton holding a scoped instance would pin one room for the life of
// the process, which is exactly what `servo generate` refuses to emit.
package api

import (
	"context"

	"example.com/servoscoped/chat"
	"example.com/servoscoped/logger"
)

type Server struct {
	rooms chat.Rooms
	log   *logger.Logger
}

func New(rooms chat.Rooms, log *logger.Logger) *Server {
	return &Server{rooms: rooms, log: log}
}

// Post is the shape every handler takes: acquire, defer the release,
// use the instance. The release is what decrements the reference count —
// not ctx ending, which fires on cancellation rather than on completion
// and would free the room while these defers were still running.
func (s *Server) Post(ctx context.Context, msg string) error {
	room, release, err := s.rooms.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	room.Post(msg)
	return nil
}

func (s *Server) Messages(ctx context.Context) ([]string, error) {
	room, release, err := s.rooms.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	return room.Messages(), nil
}

func (s *Server) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *Server) Drain(context.Context) error { return nil }

func (s *Server) Stop(context.Context) error { return nil }
