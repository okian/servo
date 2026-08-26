package graph

import (
	"go/token"
	"testing"
)

func TestComparePos(t *testing.T) {
	pos := func(file string, line, col int) token.Position {
		return token.Position{Filename: file, Line: line, Column: col}
	}

	cases := []struct {
		name string
		a, b token.Position
		want int
	}{
		{"a's file sorts first", pos("a.go", 5, 1), pos("b.go", 1, 1), -1},
		{"b's file sorts first", pos("b.go", 1, 1), pos("a.go", 5, 1), 1},
		{"same file, a's line first", pos("x.go", 1, 9), pos("x.go", 2, 1), -1},
		{"same file, b's line first", pos("x.go", 2, 1), pos("x.go", 1, 9), 1},
		{"same file and line, a's column first", pos("x.go", 1, 1), pos("x.go", 1, 2), -1},
		{"same file and line, b's column first", pos("x.go", 1, 2), pos("x.go", 1, 1), 1},
		{"identical position", pos("x.go", 1, 1), pos("x.go", 1, 1), 0},
	}
	for _, c := range cases {
		got := ComparePos(c.a, c.b)
		switch {
		case c.want < 0 && got >= 0:
			t.Errorf("%s: ComparePos = %d, want negative", c.name, got)
		case c.want > 0 && got <= 0:
			t.Errorf("%s: ComparePos = %d, want positive", c.name, got)
		case c.want == 0 && got != 0:
			t.Errorf("%s: ComparePos = %d, want 0", c.name, got)
		}
	}
}
