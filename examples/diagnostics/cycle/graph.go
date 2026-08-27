// Package cycle has A depending on B and B depending back on A, so servo
// generate fails rather than emitting code that could never construct
// either one.
package cycle

type A struct{ b *B }

func NewA(b *B) *A { return &A{b: b} }

type B struct{ a *A }

func NewB(a *A) *B { return &B{a: a} }
