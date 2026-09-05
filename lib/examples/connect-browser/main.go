// Package main ...
package main

import (
	"context"
	"fmt"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
)

// To manually launch a browser.
func main() {
	// Launch your local browser first:
	//
	//     chrome --headless --remote-debugging-port=9222
	//
	u := launcher.MustResolveURL(context.Background(), "")

	browser := rod.New().ControlURL(u).MustConnect()

	fmt.Println(
		browser.MustPage("https://mdn.dev/").MustEval("() => document.title"),
	)
}
