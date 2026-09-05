package launcher_test

import (
	"context"
	"os"
	"os/exec"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/utils"
)

func Example_use_system_browser() {
	if path, exists := launcher.LookPath(); exists {
		l := launcher.New().Bin(path)
		u := l.MustLaunch()
		defer func() {
			l.Kill()
			l.Cleanup()
		}()

		browser := rod.New().ControlURL(u).MustConnect()
		defer browser.MustClose()
	}
}

func Example_print_browser_CLI_output() {
	// Pipe the browser stderr and stdout to os.Stdout .
	l := launcher.New().Logger(os.Stdout)
	u := l.MustLaunch()
	defer func() {
		l.Kill()
		l.Cleanup()
	}()

	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()
}

func Example_custom_launch() {
	// get the browser executable path
	path, exists := launcher.LookPath()
	if !exists {
		return
	}

	// use the FormatArgs to construct args, this line is optional, you can construct the args manually
	args := launcher.New().FormatArgs()

	cmd := exec.Command(path, args...)

	parser := launcher.NewURLParser()
	cmd.Stderr = parser
	utils.E(cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	u := launcher.MustResolveURL(context.Background(), <-parser.URL)

	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()
}
