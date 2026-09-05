// Package store is the example's only real dependency: an in-memory
// category catalog the Order handler consults. It exists to show a
// //servo: handler receiving an ordinary graph-resolved singleton — and to
// give every status path in the API something true to report.
package store

import "errors"

// ErrNotFound is the domain sentinel the API layer maps to 404 — the
// store itself knows nothing about HTTP.
var ErrNotFound = errors.New("no such category")

type Category struct {
	Name string
	Open bool
}

type Store struct {
	categories map[string]Category
}

func New() *Store {
	return &Store{categories: map[string]Category{
		"espresso": {Name: "espresso", Open: true},
		"filter":   {Name: "filter", Open: true},
		// A permanently closed category, so the 403 path has a real case.
		"seasonal": {Name: "seasonal", Open: false},
	}}
}

// Category looks a category up. The "broken" name simulates an
// infrastructure failure so the 500 path — and the promise that its detail
// stays out of the response body — can be exercised end to end.
func (s *Store) Category(name string) (Category, error) {
	if name == "broken" {
		return Category{}, errors.New("simulated storage failure: db down")
	}
	c, ok := s.categories[name]
	if !ok {
		return Category{}, ErrNotFound
	}
	return c, nil
}
