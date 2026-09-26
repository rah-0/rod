package rod

import (
	"bytes"
	"encoding/base64"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/rah-0/rod/lib/proto"
)

func TestSaveFileDefaultPaths(t *testing.T) {
	for _, test := range []struct {
		name string
		kind saveFileType
		dir  string
		ext  string
	}{
		{"screenshot", saveFileTypeScreenshot, "screenshots", ".png"},
		{"PDF", saveFileTypePDF, "pdf", ".pdf"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			content := []byte("output")
			if err := saveFile(test.kind, content, []string{""}); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join("tmp", test.dir, "*"+test.ext))
			if err != nil || len(files) != 1 {
				t.Fatalf("default output files: %v, error: %v", files, err)
			}
			got, err := os.ReadFile(files[0])
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("saved content: %q, error: %v", got, err)
			}
		})
	}
}

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

func TestDecodeDataURL(t *testing.T) {
	for _, test := range []struct {
		uri, want string
	}{
		{"data:image/png;base64,AP8B", "\x00\xff\x01"},
		{"data:,", ""}, // canvas without pixels
		{"data:,a%20b%2C", "a b,"},
		{"DATA:text/plain;charset=utf-8,plain+text", "plain+text"},
		{"data:text/plain,;base64,aGk=", ";base64,aGk="},
		{"data:text/plain;base64x,aGk=", "aGk="},
		{"data:text/plain;BASE64,aGk", "hi"},
		{"data:text/plain ; base64 ,aGk=", "hi"},
		{"data:;base64,aG k=\n", "hi"},
		{"data:;base64,aGk%3D", "hi"},
		{"data:;base64,aG\r\nk=", "hi"},
		{"data:;base64,aG\tk", "hi"},
		{"data:,100%", "100%"},
		{"data:,a%zzb%4", "a%zzb%4"},
		{"data:,%41%4a%4A", "AJJ"},
		{"data:,a#b", "a#b"},
	} {
		got, err := decodeDataURL(test.uri)
		if err != nil || string(got) != test.want {
			t.Errorf("decodeDataURL(%q) = %q, %v; want %q", test.uri, got, err, test.want)
		}
	}

	for _, uri := range []string{
		"",
		"dat",
		"not a data URL",
		"data:image/png;base64",
		"data:;base64,@@@@",
		"data:;base64,aGk==",
		"data:;base64,a",
		"data:;base64,aGk=aGk=",
		"data:;base64,%zz",
	} {
		if got, err := decodeDataURL(uri); !errors.Is(err, ErrInvalidDataURL) || got != nil {
			t.Errorf("decodeDataURL(%q) = %q, %v; want ErrInvalidDataURL", uri, got, err)
		}
	}

	var corrupt base64.CorruptInputError
	if _, err := decodeDataURL("data:;base64,@@@@"); !errors.As(err, &corrupt) {
		t.Errorf("base64 decoding error is not preserved: %v", err)
	}
}

func BenchmarkDecodeDataURL(b *testing.B) {
	data := bytes.Repeat([]byte{0x89, 'P', 'N', 'G', 0, 0xff}, 16<<20/6)
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	b.SetBytes(int64(len(uri)))
	for b.Loop() {
		if got, err := decodeDataURL(uri); err != nil || len(got) != len(data) {
			b.Fatalf("decoded %d bytes, error %v", len(got), err)
		}
	}
}
