package proto_test

import (
	"testing"

	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/proto"
)

func TestPoint(t *testing.T) {
	g := testutil.New(t)
	p := proto.NewPoint(1, 2).
		Add(proto.NewPoint(3, 4)).
		Minus(proto.NewPoint(1, 1)).
		Scale(2)

	g.Eq(p.X, 6)
	g.Eq(p.Y, 10)
}

func TestShapeBox(t *testing.T) {
	g := testutil.New(t)

	g.Nil(proto.Shape{}.Box())
	// Endpoint quads without a complete point do not contribute to the box.
	g.Nil(proto.Shape{{}, {5}}.Box())
	g.Eq(*proto.Shape{{}, {1, 2, 5, 2, 5, 8, 1, 8}}.Box(), proto.DOMRect{X: 1, Y: 2, Width: 4, Height: 6})
	g.Eq(*proto.Shape{{3, 4, 7}, {1, 9}}.Box(), proto.DOMRect{X: 1, Y: 4, Width: 2, Height: 5})
	g.Eq(*(&proto.DOMGetContentQuadsResult{Quads: []proto.DOMQuad{{2}, {6, 7}}}).Box(), proto.DOMRect{X: 6, Y: 7})
}
