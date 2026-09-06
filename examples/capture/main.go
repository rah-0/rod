// Run with: go run ./examples/capture [-out DIRECTORY]
// Capture local page content as PNG and PDF, with desktop and mobile emulation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/devices"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

func main() {
	directory := flag.String("out", "", "output directory; defaults to a new temporary directory")
	flag.Parse()
	temporary := *directory == ""
	if temporary {
		var err error
		*directory, err = os.MkdirTemp("", "rod-capture-")
		if err != nil {
			log.Fatal(err)
		}
	}
	if err := run(context.Background(), os.Stdout, *directory); err != nil {
		if temporary {
			err = errors.Join(err, os.RemoveAll(*directory))
		}
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer, directory string) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	// Disable Rod's default device so the viewport and scale below are explicit.
	browser := rod.New().Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.Close()) }()

	app, err := fixture.HTML(browser, `<!doctype html>
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Capture example</title>
<style>
body { margin: 0; padding: 24px; background: #f3f4f6; font: 18px sans-serif; }
#card { width: 320px; height: 160px; box-sizing: border-box; padding: 16px; background: #17426b; color: white; }
@page { size: A4; margin: 20mm; }
</style>
<article id="card"><h1>Local report</h1><p>Ready to capture.</p></article>`, func(page *rod.Page) error {
		return page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
			Width: 800, Height: 600, DeviceScaleFactor: 1,
		})
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close()) }()
	if err := app.Page.WaitLoad(); err != nil {
		return err
	}
	// Wait for fonts and a paint opportunity, without an arbitrary delay.
	if _, err := app.Page.Eval(`async () => {
  await document.fonts.ready;
  await new Promise(requestAnimationFrame);
}`); err != nil {
		return err
	}

	viewportPNG, err := app.Page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return err
	}
	card, err := app.Page.Element("#card")
	if err != nil {
		return err
	}
	elementPNG, err := card.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
	if err != nil {
		return err
	}
	// PDF returns a protocol stream. Read and close it before closing the page.
	pdf, err := app.Page.PDF(&proto.PagePrintToPDF{PrintBackground: new(true), PreferCSSPageSize: new(true)})
	if err != nil {
		return err
	}
	pdfData, readErr := io.ReadAll(pdf)
	if err := errors.Join(readErr, pdf.Close()); err != nil {
		return err
	}

	// Device emulation changes viewport, pixel ratio, touch support, and user agent.
	if err := app.Page.Emulate(devices.IPhone6or7or8); err != nil {
		return err
	}
	if err := app.Page.Wait(rod.Eval(`() => innerWidth === 375 && innerHeight === 667 && devicePixelRatio === 2`)); err != nil {
		return err
	}
	var mobile struct {
		Width  int     `json:"width"`
		Height int     `json:"height"`
		Scale  float64 `json:"scale"`
		Touch  int     `json:"touch"`
		IPhone bool    `json:"iphone"`
	}
	if err := app.Page.EvalJSON(&mobile, `() => ({
  width: innerWidth, height: innerHeight, scale: devicePixelRatio,
  touch: navigator.maxTouchPoints, iphone: navigator.userAgent.includes('iPhone')
})`); err != nil {
		return err
	}
	if mobile.Touch == 0 || !mobile.IPhone {
		return fmt.Errorf("mobile emulation was not applied: %+v", mobile)
	}
	mobilePNG, err := app.Page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return err
	}

	for _, artifact := range []struct {
		Name string
		Data []byte
	}{
		{"page.png", viewportPNG},
		{"element.png", elementPNG},
		{"mobile.png", mobilePNG},
		{"page.pdf", pdfData},
	} {
		if err := os.WriteFile(filepath.Join(directory, artifact.Name), artifact.Data, 0o644); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(output, "Desktop viewport: 800x600 @1x\nMobile viewport: %dx%d @%gx\nSaved page.png, element.png, mobile.png, page.pdf\nArtifacts: %s\n", mobile.Width, mobile.Height, mobile.Scale, directory)
	return err
}
