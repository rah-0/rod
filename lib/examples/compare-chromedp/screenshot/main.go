// Package main ...
package main

import (
	"os"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
)

// This example demonstrates how to take a screenshot of a specific element and
// of the entire browser viewport, as well as using `kit`
// to store it into a file.
func main() {
	browser := rod.New().MustConnect()

	// capture screenshot of an element
	browser.MustPage("https://google.com").MustElement("body div").MustScreenshot("elementScreenshot.png")

	// capture entire browser viewport, returning jpg with quality=90
	buf, err := browser.MustPage("https://brank.as/").Screenshot(true, &proto.PageCaptureScreenshot{
		Format:  proto.PageCaptureScreenshotFormatJpeg,
		Quality: jsonvalue.Int(90),
	})
	if err != nil {
		panic(err)
	}

	err = os.WriteFile("fullScreenshot.png", buf, 0o644)
	if err != nil {
		panic(err)
	}
}
