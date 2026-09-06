package utils

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/rah-0/rod/lib/proto"
)

func TestSplicePngHorizontalCrops(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 5, 2))
	for y := range 2 {
		for x := range 5 {
			source.SetRGBA(x, y, color.RGBA{R: uint8(20 + x), G: uint8(y), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	first, second := image.Rect(2, 1, 5, 2), image.Rect(1, 0, 3, 1)
	data, err := SplicePngVertical([]ImgWithBox{
		{Img: encoded.Bytes(), Box: &first}, {Img: encoded.Bytes(), Box: &second},
	}, proto.PageCaptureScreenshotFormatPng, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if result.Bounds() != image.Rect(0, 0, 3, 2) {
		t.Fatalf("bounds = %v", result.Bounds())
	}
	for row, box := range []image.Rectangle{first, second} {
		for x := range box.Dx() {
			got := color.RGBAModel.Convert(result.At(x, row))
			want := color.RGBAModel.Convert(source.At(box.Min.X+x, box.Min.Y))
			if got != want {
				t.Fatalf("pixel %d,%d = %v, want %v", x, row, got, want)
			}
		}
	}
}
