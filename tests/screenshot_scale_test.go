package rod_test

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rah-0/rod/lib/proto"
)

func TestElementScreenshotScale(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/frame" {
			_, _ = w.Write([]byte(`<style>body{margin:0;background:white}#box{position:absolute;left:40px;top:30px;width:100px;height:50px;background:rgb(255,0,0)}</style><div id="box"></div>`))
			return
		}
		_, _ = w.Write([]byte(`<style>body{margin:0;background:white;min-height:2200px}#box{position:absolute;left:200px;top:120px;width:100px;height:50px;background:rgb(255,0,0)}iframe{position:absolute;left:100px;top:1700px;width:300px;height:200px;border:0}</style><div id="box"></div><iframe src="/frame"></iframe>`))
	}))
	defer server.Close()
	for _, scale := range []float64{1, 1.5, 2} {
		for _, scenario := range []string{"positioned", "scrolled", "transformed", "zoomed", "fractional", "iframe", "jpeg"} {
			t.Run(fmt.Sprintf("scale-%g/%s", scale, scenario), func(t *testing.T) {
				g := setup(t)
				page := g.page
				g.E(page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: 800, Height: 600, DeviceScaleFactor: scale}))
				page.MustNavigate(server.URL).MustWaitLoad()
				element := page.MustElement("#box")
				width, height := 100.0, 50.0
				switch scenario {
				case "scrolled":
					element.MustEval(`() => this.style.top = '1300px'`)
				case "transformed":
					element.MustEval(`() => { this.style.top = '1300px'; this.style.transform = 'scale(1.2)' }`)
					width, height = 120, 60
				case "zoomed":
					page.MustEval(`() => document.body.style.zoom = '1.2'`)
					width, height = 120, 60
				case "fractional":
					element.MustEval(`() => { this.style.left = '200.25px'; this.style.top = '120.25px'; this.style.width = '100.5px'; this.style.height = '50.5px' }`)
					width, height = 101, 51 // enclosing DIP edges preserve fractional content
				case "iframe":
					element = page.MustElement("iframe").MustFrame().MustElement("#box")
				}
				format := proto.PageCaptureScreenshotFormatPng
				if scenario == "jpeg" {
					format = proto.PageCaptureScreenshotFormatJpeg
				}
				data, err := element.Screenshot(format, 95)
				g.E(err)
				bitmap, decoded, err := image.Decode(bytes.NewReader(data))
				g.E(err)
				wantFormat := "png"
				if scenario == "jpeg" {
					wantFormat = "jpeg"
				}
				if decoded != wantFormat {
					t.Fatalf("format: %s, want %s", decoded, wantFormat)
				}
				bounds := bitmap.Bounds()
				if bounds.Dx() != int(math.Round(width*scale)) || bounds.Dy() != int(math.Round(height*scale)) {
					t.Fatalf("bitmap bounds: %v, want %.0fx%.0f", bounds, math.Round(width*scale), math.Round(height*scale))
				}
				for _, point := range []image.Point{{bounds.Dx() / 2, bounds.Dy() / 2}, {3, 3}, {bounds.Dx() - 4, bounds.Dy() - 4}} {
					r, green, blue, _ := bitmap.At(point.X, point.Y).RGBA()
					if r < 60000 || green > 4000 || blue > 4000 {
						t.Fatalf("pixel %v: %d,%d,%d; expected red element", point, r, green, blue)
					}
				}
			})
		}
	}
}
