package rod

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"

	"github.com/rah-0/rod/lib/proto"
)

// MaxScreenshotPixels bounds full-page and clipped captures before requesting
// them, and PNG/JPEG bitmap dimensions before returning them.
const MaxScreenshotPixels = 32 << 20

// MaxCaptureBytes bounds an encoded screenshot and a PDF stream by default.
const MaxCaptureBytes = 64 << 20

var (
	// ErrScreenshotTooLarge means a capture exceeds the pixel budget or has
	// invalid dimensions. Clip or scroll the page to capture smaller regions.
	ErrScreenshotTooLarge = errors.New("rod: screenshot exceeds pixel budget")
	// ErrCaptureTooLarge means an artifact exceeds its byte budget.
	ErrCaptureTooLarge = errors.New("rod: capture exceeds byte budget")
)

func (p *Page) checkScreenshotSize(width, height, scale float64) error {
	view := proto.EmulationSetDeviceMetricsOverride{}
	if p.LoadState(&view) && view.DeviceScaleFactor > 0 {
		scale *= view.DeviceScaleFactor
	}
	width, height = math.Ceil(width*scale), math.Ceil(height*scale)
	if math.IsNaN(width) || math.IsNaN(height) || width <= 0 || height <= 0 ||
		width > 32768 || height > 32768 || width*height > MaxScreenshotPixels {
		return ErrScreenshotTooLarge
	}
	return nil
}

func checkScreenshotData(data []byte) error {
	if len(data) > MaxCaptureBytes {
		return ErrCaptureTooLarge
	}
	// Chrome also supports WebP, which has no decoder in the standard library.
	// Its dimensions are guarded before the request; all formats have a byte cap.
	if config, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		if config.Width > 32768 || config.Height > 32768 ||
			int64(config.Width)*int64(config.Height) > MaxScreenshotPixels {
			return ErrScreenshotTooLarge
		}
	}
	return nil
}
