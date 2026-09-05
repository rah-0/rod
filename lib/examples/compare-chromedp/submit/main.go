// Package main ...
package main

import (
	"log"
	"strings"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/input"
)

// This example demonstrates how to fill out and submit a form.
func main() {
	page := rod.New().MustConnect().MustPage("https://github.com/search")

	page.MustElement(`input[name=q]`).MustWaitVisible().MustInput("chromedp").MustType(input.Enter)

	res := page.MustElementR("a", "chromedp").MustParent().MustParent().MustNext().MustText()

	log.Printf("got: `%s`", strings.TrimSpace(res))
}
