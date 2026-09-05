// Package main ...
package main

import (
	"fmt"
	"os"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/utils"
)

func main() {
	// This example is to launch a browser remotely, not connect to a running browser remotely,
	// to connect to a running browser check the "../connect-browser" example.
	// Start a launcher.Manager on a machine with Chrome or Chromium installed:
	//
	//     export ROD_MANAGER_TOKEN="$(openssl rand -hex 32)"
	//     go run ./lib/launcher/rod-manager
	//
	// For available CLI flags run: go run ./lib/launcher/rod-manager -h
	// For more information, check the doc of launcher.Manager
	authToken := os.Getenv(launcher.ManagerTokenEnv)
	l := launcher.MustNewManaged("", authToken)

	// You can also set browser flags remotely before you launch the remote browser.
	// Process settings such as the executable, environment, working directory, and XVFB are server-owned.
	// Available flags: https://peter.sh/experiments/chromium-command-line-switches
	l.Set("disable-gpu").Delete("disable-gpu")

	browser := rod.New().Client(l.MustClient()).MustConnect()

	// You may want to start a server to watch the screenshots of the remote browser.
	launcher.Open(browser.ServeMonitor(""))

	fmt.Println(
		browser.MustPage("https://developer.mozilla.org").MustEval("() => document.title"),
	)

	// Launch another browser with the same manager.
	ll := launcher.MustNewManaged("", authToken)

	// You can set different flags for each browser.
	ll.Set("disable-sync").Delete("disable-sync")

	anotherBrowser := rod.New().Client(ll.MustClient()).MustConnect()

	fmt.Println(
		anotherBrowser.MustPage("https://github.com/rah-0/rod").MustEval("() => document.title"),
	)

	utils.Pause()
}
