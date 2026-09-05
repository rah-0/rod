package testutil

import "testing"

var (
	_ Testable = (*testing.T)(nil)
	_ Testable = (*testing.B)(nil)
)

func run(g G, name string, f func(G)) bool {
	switch t := g.Testable.(type) {
	case *testing.T:
		return t.Run(name, func(t *testing.T) { f(New(t)) })
	case *testing.B:
		return t.Run(name, func(b *testing.B) { f(New(b)) })
	default:
		g.Fatalf("testutil: %T does not support subtests", g.Testable)
		return false
	}
}
