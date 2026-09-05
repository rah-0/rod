package rod

import (
	"math"
	"testing"

	"github.com/rah-0/rod/lib/proto"
)

func TestShapesEqual(t *testing.T) {
	shape := func(quads ...proto.DOMQuad) *proto.DOMGetContentQuadsResult {
		return &proto.DOMGetContentQuadsResult{Quads: quads}
	}
	nanShape := shape(proto.DOMQuad{math.NaN()})
	for _, tc := range []struct {
		name string
		a, b *proto.DOMGetContentQuadsResult
		want bool
	}{
		{"nil results", nil, nil, true},
		{"first frame", nil, shape(), false},
		{"empty quad lists", shape(), &proto.DOMGetContentQuadsResult{Quads: []proto.DOMQuad{}}, true},
		{"empty coordinates", shape(nil), shape(proto.DOMQuad{}), true},
		{"quad count", shape(), shape(nil), false},
		{"coordinate count", shape(proto.DOMQuad{1, 2}), shape(proto.DOMQuad{1}), false},
		{"unchanged", shape(proto.DOMQuad{10, 20, 110, 20, 110, 70, 10, 70}), shape(proto.DOMQuad{10, 20, 110, 20, 110, 70, 10, 70}), true},
		{"changed coordinate", shape(proto.DOMQuad{10, 20, 110, 20, 110, 70, 10, 70}), shape(proto.DOMQuad{10, 20, 110, 20, 110, 70, 11, 70}), false},
		{"quad order", shape(proto.DOMQuad{1}, proto.DOMQuad{2}), shape(proto.DOMQuad{2}, proto.DOMQuad{1}), false},
		{"NaN", nanShape, shape(proto.DOMQuad{math.NaN()}), false},
		{"shared NaN", nanShape, nanShape, false},
		{"signed zero", shape(proto.DOMQuad{0}), shape(proto.DOMQuad{math.Copysign(0, -1)}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shapesEqual(tc.a, tc.b); got != tc.want {
				t.Fatalf("shapesEqual(%v, %v) = %v; want %v", tc.a, tc.b, got, tc.want)
			}
			if got := shapesEqual(tc.b, tc.a); got != tc.want {
				t.Fatalf("reversed shapesEqual = %v; want %v", got, tc.want)
			}
		})
	}
}

var shapeComparisonResult bool

func BenchmarkShapesEqual(b *testing.B) {
	for _, tc := range []struct {
		name    string
		current proto.DOMQuad
	}{
		{"unchanged", proto.DOMQuad{10, 20, 110, 20, 110, 70, 10, 70}},
		{"moved", proto.DOMQuad{11, 20, 111, 20, 111, 70, 11, 70}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			previous := &proto.DOMGetContentQuadsResult{Quads: []proto.DOMQuad{{10, 20, 110, 20, 110, 70, 10, 70}}}
			current := &proto.DOMGetContentQuadsResult{Quads: []proto.DOMQuad{tc.current}}
			b.ReportAllocs()
			for b.Loop() {
				shapeComparisonResult = shapesEqual(previous, current)
			}
		})
	}
}
