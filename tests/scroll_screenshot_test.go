package rod_test

import (
	"bytes"
	"errors"
	"image/color"
	"image/png"
	"math"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/jsonvalue"
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

func TestScrollScreenshotValidatesBounds(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><style>html,body{margin:0}</style><div id="content" style="height:250px"></div>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	g.E(p.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: 160, Height: 100, DeviceScaleFactor: 1}))
	p.MustWaitLoad()

	for name, options := range map[string]*rod.ScrollScreenshotOptions{
		"fixed areas cover the viewport": {FixedTop: 60, FixedBottom: 40},
		"negative fixed area":            {FixedTop: -1},
		"NaN fixed area":                 {FixedBottom: math.NaN()},
		"infinite fixed area":            {FixedTop: math.Inf(1)},
		"negative wait":                  {WaitPerScroll: -time.Millisecond},
		"negative height limit":          {MaxHeight: -1},
	} {
		if _, err := p.ScrollScreenshot(options); !errors.Is(err, rod.ErrScrollScreenshotOptions) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	options := &rod.ScrollScreenshotOptions{MaxHeight: 200}
	if _, err := p.ScrollScreenshot(options); !errors.Is(err, rod.ErrScrollScreenshotTooTall) {
		t.Fatalf("content taller than MaxHeight: %v", err)
	}
	if options.MaxHeight != 200 || options.WaitPerScroll != 0 {
		t.Fatalf("options were modified: %+v", options)
	}
	options.MaxHeight = 250
	data, err := p.ScrollScreenshot(options)
	g.E(err)
	bitmap, err := png.Decode(bytes.NewReader(data))
	g.E(err)
	if bitmap.Bounds().Dy() != 250 {
		t.Fatalf("unexpected stitched bounds: %v", bitmap.Bounds())
	}

	p.MustEval(`() => document.getElementById('content').style.height = '40000px'`)
	if _, err := p.ScrollScreenshot(nil); !errors.Is(err, rod.ErrScrollScreenshotTooTall) {
		t.Fatalf("content taller than the default limit: %v", err)
	}

	g.mc.stub(1, proto.PageGetLayoutMetrics{}, func(StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(layoutMetrics(&proto.PageVisualViewport{ClientWidth: 160}, &proto.DOMRect{Height: 250})), nil
	})
	if _, err := p.ScrollScreenshot(nil); err == nil || errors.Is(err, proto.ErrMissingField) {
		t.Fatalf("zero viewport height accepted: %v", err)
	}

	// Without a scroll step, content shorter than the viewport was captured
	// repeatedly until the context ended.
	g.mc.stub(1, proto.PageGetLayoutMetrics{}, func(StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(layoutMetrics(&proto.PageVisualViewport{ClientWidth: 160, ClientHeight: 100}, &proto.DOMRect{Height: 50})), nil
	})
	if _, err := p.ScrollScreenshot(&rod.ScrollScreenshotOptions{FixedTop: 60, FixedBottom: 40}); !errors.Is(err, rod.ErrScrollScreenshotOptions) {
		t.Fatalf("fixed areas covering the viewport of short content: %v", err)
	}
}

// layoutMetrics is a complete Page.getLayoutMetrics result with the given CSS
// viewport and content size.
func layoutMetrics(viewport *proto.PageVisualViewport, content *proto.DOMRect) proto.PageGetLayoutMetricsResult {
	return proto.PageGetLayoutMetricsResult{
		LayoutViewport: &proto.PageLayoutViewport{}, VisualViewport: &proto.PageVisualViewport{}, ContentSize: &proto.DOMRect{},
		CSSLayoutViewport: &proto.PageLayoutViewport{}, CSSVisualViewport: viewport, CSSContentSize: content,
	}
}

// expectMissingLayoutMetric expects err to report that a Page.getLayoutMetrics
// result lacks the object at path.
func expectMissingLayoutMetric(t *testing.T, err error, path string) {
	t.Helper()
	missing, ok := errors.AsType[*proto.MissingFieldError](err)
	if !ok || !errors.Is(err, proto.ErrMissingField) || missing.Type != "PageGetLayoutMetricsResult" || missing.Path != path {
		t.Fatalf("layout metrics without %s: %v", path, err)
	}
}
