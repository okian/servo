package api

import "example.com/servomocking/store"

type Server struct{ store store.Store }

func New(s store.Store) *Server { return &Server{store: s} }

func (s *Server) Lookup(key string) string { return s.store.Get(key) }
