// Package main ...
package main

import (
	"fmt"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
)

func main() {
	l := launcher.New()

	// For more info: https://pkg.go.dev/github.com/rah-0/rod/lib/launcher
	u := l.MustLaunch()
	defer func() {
		l.Kill()
		l.Cleanup()
	}()

	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()

	page := browser.MustPage("http://example.com").MustWaitStable()

	fmt.Println(page.MustInfo().Title)
}
