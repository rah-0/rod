package launcher_test

import (
	"os"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
)

func Example_use_system_browser() {
	if path, exists := launcher.LookPath(); exists {
		l := launcher.New().Bin(path)
		defer func() {
			l.Kill()
			l.Cleanup()
		}()
		u := l.MustLaunch()

		browser := rod.New().ControlURL(u).MustConnect()
		defer func() {
			browser.Timeout(5 * time.Second).MustClose()
		}()
	}
}

func Example_print_browser_CLI_output() {
	// Pipe the browser stderr and stdout to os.Stdout .
	l := launcher.New().Logger(os.Stdout)
	defer func() {
		l.Kill()
		l.Cleanup()
	}()
	u := l.MustLaunch()

	browser := rod.New().ControlURL(u).MustConnect()
	defer func() {
		browser.Timeout(5 * time.Second).MustClose()
	}()
}

func Example_custom_launch() {
	// get the browser executable path
	path, exists := launcher.LookPath()
	if !exists {
		return
	}

	// Keep process and profile ownership when customizing the browser command.
	l := launcher.New().Bin(path).Set("disable-gpu").Logger(os.Stdout)
	defer func() {
		l.Kill()
		l.Cleanup()
	}()
	u := l.MustLaunch()

	browser := rod.New().ControlURL(u).MustConnect()
	defer func() {
		browser.Timeout(5 * time.Second).MustClose()
	}()
}
