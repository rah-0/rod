package proto_test

import (
	"reflect"
	"testing"

	"github.com/rah-0/rod/lib/proto"
)

func TestQuadAreaIgnoresWinding(t *testing.T) {
	for _, quad := range []proto.DOMQuad{
		{0, 0, 4, 0, 4, 3, 0, 3},
		{4, 0, 0, 0, 0, 3, 4, 3},
	} {
		if quad.Area() != 12 {
			t.Fatalf("area of %v = %v, want 12", quad, quad.Area())
		}
		shape := &proto.DOMGetContentQuadsResult{Quads: []proto.DOMQuad{quad}}
		if point := shape.OnePointInside(); point == nil || *point != (proto.Point{X: 2, Y: 1.5}) {
			t.Fatalf("inside point = %v", point)
		}
	}
	if (proto.DOMQuad{}).Area() != 0 {
		t.Fatal("empty polygon has nonzero area")
	}
}

func TestCookieConversionPreservesScope(t *testing.T) {
	cookie := &proto.NetworkCookie{
		Name: "session", Value: "example", Domain: ".service.test", Path: "/account",
		Secure: true, HTTPOnly: true, SameSite: proto.NetworkCookieSameSiteNone,
		SameParty: true, SourceScheme: proto.NetworkCookieSourceSchemeSecure, SourcePort: 8443,
		PartitionKey: &proto.NetworkCookiePartitionKey{
			TopLevelSite: "https://example.test", HasCrossSiteAncestor: true,
		},
	}
	converted := proto.CookiesToParams([]*proto.NetworkCookie{cookie})[0]
	if !reflect.DeepEqual(converted.PartitionKey, cookie.PartitionKey) ||
		converted.SourceScheme != cookie.SourceScheme || converted.SourcePort == nil ||
		*converted.SourcePort != cookie.SourcePort || !converted.SameParty || !converted.HTTPOnly ||
		converted.Domain != cookie.Domain || converted.Path != cookie.Path {
		t.Fatalf("cookie scope changed: %+v", converted)
	}
	plain := proto.CookiesToParams([]*proto.NetworkCookie{{Name: "plain", SourcePort: -1}})[0]
	if plain.PartitionKey != nil || plain.SourcePort == nil || *plain.SourcePort != -1 {
		t.Fatalf("unpartitioned cookie scope changed: %+v", plain)
	}
}
