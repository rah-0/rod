package utils

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/jpeg"
	"image/png"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/rah-0/rod/lib/proto"
)

// spliceReference is the per-pixel implementation that SplicePngVertical must match.
func spliceReference(t *testing.T, files []ImgWithBox, format proto.PageCaptureScreenshotFormat) []byte {
	t.Helper()
	processor, err := NewImgProcessor(format)
	if err != nil {
		t.Fatal(err)
	}
	var width, height int
	var images []image.Image
	for _, file := range files {
		img, err := processor.Decode(bytes.NewReader(file.Img))
		if err != nil {
			t.Fatal(err)
		}
		images = append(images, img)
		bounds := img.Bounds()
		if file.Box != nil {
			bounds = *file.Box
		}
		width = max(width, bounds.Dx())
		height += bounds.Dy()
	}
	result := image.NewRGBA(image.Rect(0, 0, width, height))
	var destY int
	for i, file := range files {
		bounds := images[i].Bounds()
		if file.Box != nil {
			bounds = *file.Box
		}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				result.Set(x-bounds.Min.X, y-bounds.Min.Y+destY, images[i].At(x, y))
			}
		}
		destY += bounds.Dy()
	}
	data, err := processor.Encode(result, nil)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// patternImage fills an image of the given kind with varying colors and alpha.
func patternImage(kind string, width, height int) image.Image {
	bounds := image.Rect(0, 0, width, height)
	pixel := func(x, y int) color.NRGBA {
		return color.NRGBA{R: uint8(x * 7), G: uint8(y * 13), B: uint8(x ^ y), A: uint8(128 + (x+y)%128)}
	}
	var img interface {
		image.Image
		Set(x, y int, c color.Color)
	}
	switch kind {
	case "rgba":
		img = image.NewRGBA(bounds)
	case "nrgba":
		img = image.NewNRGBA(bounds)
	case "nrgba64":
		img = image.NewNRGBA64(bounds)
	case "gray":
		img = image.NewGray(bounds)
	case "paletted":
		img = image.NewPaletted(bounds, palette.WebSafe)
	default:
		panic(kind)
	}
	for y := range height {
		for x := range width {
			c := pixel(x, y)
			if kind == "rgba" {
				c.A = 255
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func encodeImage(t testing.TB, format proto.PageCaptureScreenshotFormat, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	var err error
	if format == proto.PageCaptureScreenshotFormatJpeg {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&buf, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSplicePngVerticalMatchesPixelCopy(t *testing.T) {
	boxes := map[string][]*image.Rectangle{
		"whole":   {nil, nil, nil},
		"cropped": {new(image.Rect(3, 2, 17, 9)), nil, new(image.Rect(0, 5, 11, 12))},
		"empty":   {new(image.Rect(4, 4, 4, 4)), new(image.Rect(0, 0, 16, 15)), nil},
	}
	for _, format := range []proto.PageCaptureScreenshotFormat{proto.PageCaptureScreenshotFormatPng, proto.PageCaptureScreenshotFormatJpeg} {
		for _, kind := range []string{"rgba", "nrgba", "nrgba64", "gray", "paletted"} {
			for name, box := range boxes {
				t.Run(fmt.Sprintf("%s/%s/%s", format, kind, name), func(t *testing.T) {
					files := []ImgWithBox{
						{Img: encodeImage(t, format, patternImage(kind, 20, 12)), Box: box[0]},
						{Img: encodeImage(t, format, patternImage(kind, 16, 15)), Box: box[1]},
						{Img: encodeImage(t, format, patternImage(kind, 23, 13)), Box: box[2]},
					}
					got, err := SplicePngVertical(files, format, nil)
					if err != nil {
						t.Fatal(err)
					}
					if want := spliceReference(t, files, format); !bytes.Equal(got, want) {
						t.Fatal("spliced image differs from the per-pixel copy")
					}
				})
			}
		}
	}
}

func TestSplicePngVerticalBoxes(t *testing.T) {
	small := encodeImage(t, proto.PageCaptureScreenshotFormatPng, patternImage("rgba", 4, 4))
	for name, box := range map[string]image.Rectangle{
		"inverted": {Min: image.Pt(3, 3), Max: image.Pt(1, 4)},
		"outside":  image.Rect(-1, 0, 4, 4),
		"larger":   image.Rect(0, 0, 1<<40, 1<<40),
	} {
		t.Run(name, func(t *testing.T) {
			files := []ImgWithBox{{Img: small, Box: &box}, {Img: small}}
			if _, err := SplicePngVertical(files, proto.PageCaptureScreenshotFormatPng, nil); err == nil {
				t.Fatalf("box %v accepted", box)
			}
		})
	}
}

func TestSpliceBoundsLimits(t *testing.T) {
	for name, test := range map[string]struct {
		boxes  []image.Rectangle
		format proto.PageCaptureScreenshotFormat
		ok     bool
	}{
		"limit":           {boxes: []image.Rectangle{image.Rect(0, 0, 1<<14, 1<<13), image.Rect(0, 0, 1, 1<<13)}, ok: true},
		"pixels":          {boxes: []image.Rectangle{image.Rect(0, 0, 1<<14, 1<<13), image.Rect(0, 0, 1, 1<<13+1)}},
		"height overflow": {boxes: []image.Rectangle{image.Rect(0, 0, 1, math.MaxInt), image.Rect(0, 0, 1, math.MaxInt)}},
		"jpeg height":     {boxes: []image.Rectangle{image.Rect(0, 0, 4, 40000), image.Rect(0, 0, 4, 40000)}, format: proto.PageCaptureScreenshotFormatJpeg},
		"png height":      {boxes: []image.Rectangle{image.Rect(0, 0, 4, 40000), image.Rect(0, 0, 4, 40000)}, ok: true},
	} {
		t.Run(name, func(t *testing.T) {
			bounds, err := spliceBounds(test.boxes, test.format)
			if (err == nil) != test.ok {
				t.Fatalf("bounds %v, error %v", bounds, err)
			}
		})
	}
}

func TestSplicePngVerticalChecksSizesBeforeDecoding(t *testing.T) {
	// Blank tiles compress well, but their pixels exceed the limit together.
	tile := encodeImage(t, proto.PageCaptureScreenshotFormatPng, image.NewGray(image.Rect(0, 0, 4096, 4096)))
	files := make([]ImgWithBox, MaxSplicePixels/(4096*4096)+1)
	for i := range files {
		files[i] = ImgWithBox{Img: tile}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := SplicePngVertical(files, proto.PageCaptureScreenshotFormatPng, nil)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("result larger than MaxSplicePixels accepted")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("allocated %d bytes before rejecting the result", allocated)
	}

	// A header that declares too many pixels is rejected without decoding its
	// data, which is missing here.
	huge := encodeImage(t, proto.PageCaptureScreenshotFormatPng, image.NewGray(image.Rect(0, 0, 1, 1)))
	huge = huge[:8+8+13+4]
	binary.BigEndian.PutUint32(huge[16:], 20000)
	binary.BigEndian.PutUint32(huge[20:], 20000)
	binary.BigEndian.PutUint32(huge[29:], crc32.ChecksumIEEE(huge[12:29]))
	_, err = SplicePngVertical([]ImgWithBox{{Img: huge}, {Img: tile}}, proto.PageCaptureScreenshotFormatPng, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("image larger than MaxSplicePixels: %v", err)
	}

	// Pixel data that fails to decode after a valid header is an error.
	small := encodeImage(t, proto.PageCaptureScreenshotFormatPng, patternImage("rgba", 4, 4))
	truncated := small[:8+8+13+4]
	_, err = SplicePngVertical([]ImgWithBox{{Img: small}, {Img: truncated}}, proto.PageCaptureScreenshotFormatPng, nil)
	if err == nil {
		t.Fatal("truncated image accepted")
	}
}

// BenchmarkSplicePngVertical stitches ten 1920x1080 screenshots.
func BenchmarkSplicePngVertical(b *testing.B) {
	for _, format := range []proto.PageCaptureScreenshotFormat{proto.PageCaptureScreenshotFormatPng, proto.PageCaptureScreenshotFormatJpeg} {
		b.Run(string(format), func(b *testing.B) {
			tile := encodeImage(b, format, patternImage("rgba", 1920, 1080))
			files := make([]ImgWithBox, 10)
			for i := range files {
				files[i] = ImgWithBox{Img: tile}
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := SplicePngVertical(files, format, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
