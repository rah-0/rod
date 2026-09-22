package rod_test

import (
	"bytes"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

func TestScrollScreenshotPreservesPartialTile(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><style>
		html,body{margin:0}div{height:80px}
		</style><div style="background:rgb(255,0,0)"></div><div style="background:rgb(0,255,0)"></div><div style="height:90px;background:rgb(0,0,255)"></div>`)).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	g.E(p.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: 160, Height: 100, DeviceScaleFactor: 1}))
	p.MustWaitLoad()
	data, err := p.ScrollScreenshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	bitmap, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if bitmap.Bounds().Dy() != 250 || bitmap.Bounds().Dx() < 140 || bitmap.Bounds().Dx() > 160 {
		t.Fatalf("unexpected stitched bounds: %v", bitmap.Bounds())
	}
	for _, sample := range []struct {
		y    int
		want color.RGBA
	}{
		{0, color.RGBA{R: 255, A: 255}},
		{79, color.RGBA{R: 255, A: 255}},
		{80, color.RGBA{G: 255, A: 255}},
		{159, color.RGBA{G: 255, A: 255}},
		{160, color.RGBA{B: 255, A: 255}},
		{249, color.RGBA{B: 255, A: 255}},
	} {
		if got := color.RGBAModel.Convert(bitmap.At(20, sample.y)); got != sample.want {
			t.Errorf("stitched row %d: got %v, want %v", sample.y, got, sample.want)
		}
	}
}
