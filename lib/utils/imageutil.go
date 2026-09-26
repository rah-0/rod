package utils

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/rah-0/rod/lib/proto"
)

// ImgWithBox is a image with a box, if the box is nil, it means the whole image.
type ImgWithBox struct {
	Img []byte
	Box *image.Rectangle
}

// ImgOption is the option for image processing.
type ImgOption struct {
	Quality int
}

// ImgProcessor is the interface for image processing.
type ImgProcessor interface {
	Encode(img image.Image, opt *ImgOption) ([]byte, error)
	Decode(file io.Reader) (image.Image, error)
}

type jpegProcessor struct{}

func (p jpegProcessor) Encode(img image.Image, opt *ImgOption) ([]byte, error) {
	var buf bytes.Buffer
	var jpegOpt *jpeg.Options
	if opt != nil {
		jpegOpt = &jpeg.Options{Quality: opt.Quality}
	}
	err := jpeg.Encode(&buf, img, jpegOpt)
	return buf.Bytes(), err
}

func (p jpegProcessor) Decode(file io.Reader) (image.Image, error) {
	return jpeg.Decode(file)
}

type pngProcessor struct{}

func (p pngProcessor) Encode(img image.Image, _ *ImgOption) ([]byte, error) {
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

func (p pngProcessor) Decode(file io.Reader) (image.Image, error) {
	return png.Decode(file)
}

// NewImgProcessor create a ImgProcessor by the format.
func NewImgProcessor(format proto.PageCaptureScreenshotFormat) (ImgProcessor, error) {
	switch format {
	case proto.PageCaptureScreenshotFormatJpeg:
		return &jpegProcessor{}, nil
	case "", proto.PageCaptureScreenshotFormatPng:
		return &pngProcessor{}, nil
	default:
		return nil, fmt.Errorf("not support format: %v", format)
	}
}

// MaxSplicePixels is the largest number of pixels in an image stitched by
// [SplicePngVertical], and in each image it stitches. The stitched image takes
// four bytes per pixel.
const MaxSplicePixels = 1 << 28

// SplicePngVertical splice png vertically, if there is only one image, it will return the image directly.
// Only support png and jpeg format yet, webP is not supported because no suitable processing
// library was found in golang.
//
// Each Box selects the region of its image to stitch. SplicePngVertical checks
// the sizes in the image headers before decoding any pixels, then decodes one
// image at a time. It returns an error for a box that is inverted or not
// within its image, for an image or a result larger than [MaxSplicePixels],
// and for a JPEG result wider or taller than 65535 pixels.
func SplicePngVertical(files []ImgWithBox, format proto.PageCaptureScreenshotFormat, opt *ImgOption) ([]byte, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) == 1 {
		return files[0].Img, nil
	}

	processor, err := NewImgProcessor(format)
	if err != nil {
		return nil, err
	}

	bounds := make([]image.Rectangle, len(files))
	boxes := make([]image.Rectangle, len(files))
	for i, file := range files {
		bounds[i], err = imageBounds(file.Img, format)
		if err != nil {
			return nil, err
		}
		boxes[i] = bounds[i]
		if file.Box != nil {
			boxes[i] = *file.Box
		}
		if boxes[i].Dx() < 0 || boxes[i].Dy() < 0 || !boxes[i].In(bounds[i]) {
			return nil, fmt.Errorf("image box %v is not within the image bounds %v", boxes[i], bounds[i])
		}
	}
	size, err := spliceBounds(boxes, format)
	if err != nil {
		return nil, err
	}

	spliceImg := image.NewRGBA(size)

	var destY int
	for i, file := range files {
		img, err := processor.Decode(bytes.NewReader(file.Img))
		if err != nil {
			return nil, err
		}
		dest := image.Rect(0, destY, boxes[i].Dx(), destY+boxes[i].Dy())
		draw.Draw(spliceImg, dest, img, boxes[i].Min, draw.Src)
		destY += boxes[i].Dy()
	}

	bs, err := processor.Encode(spliceImg, opt)
	if err != nil {
		return nil, err
	}

	return bs, nil
}

// imageBounds returns the bounds in an image header, checking the pixel limit.
func imageBounds(data []byte, format proto.PageCaptureScreenshotFormat) (image.Rectangle, error) {
	var config image.Config
	var err error
	if format == proto.PageCaptureScreenshotFormatJpeg {
		config, err = jpeg.DecodeConfig(bytes.NewReader(data))
	} else {
		config, err = png.DecodeConfig(bytes.NewReader(data))
	}
	if err != nil {
		return image.Rectangle{}, err
	}
	if config.Width > 0 && config.Height > MaxSplicePixels/config.Width {
		return image.Rectangle{}, fmt.Errorf("image of %dx%d pixels exceeds %d pixels", config.Width, config.Height, MaxSplicePixels)
	}
	return image.Rect(0, 0, config.Width, config.Height), nil
}

// spliceBounds returns the bounds of boxes stacked vertically, checking the
// size limits before allocation.
func spliceBounds(boxes []image.Rectangle, format proto.PageCaptureScreenshotFormat) (image.Rectangle, error) {
	var width, height int
	for _, box := range boxes {
		width = max(width, box.Dx())
		if box.Dy() > MaxSplicePixels-height {
			return image.Rectangle{}, fmt.Errorf("spliced image height exceeds %d pixels", MaxSplicePixels)
		}
		height += box.Dy()
	}
	if width > 0 && height > MaxSplicePixels/width {
		return image.Rectangle{}, fmt.Errorf("spliced image of %dx%d pixels exceeds %d pixels", width, height, MaxSplicePixels)
	}
	if format == proto.PageCaptureScreenshotFormatJpeg && (width > 65535 || height > 65535) {
		return image.Rectangle{}, fmt.Errorf("spliced image of %dx%d pixels exceeds the JPEG limit of 65535 pixels per side", width, height)
	}
	return image.Rect(0, 0, width, height), nil
}
