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
