// Run with: go run ./examples/screencast -out /tmp/screencast.tar
// Record a local animation and its pauses as timestamped PNG frames.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

//go:embed page.html
var pageHTML string

func main() {
	defaults.Load()
	path := flag.String("out", "screencast.tar", "new output archive path")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Stdout, *path); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer, path string) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	browser := rod.New().ControlURL("").Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.CloseWithTimeout(5*time.Second)) }()
	app, err := fixture.HTML(browser, pageHTML, func(page *rod.Page) error {
		return page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
			Width: 640, Height: 360, DeviceScaleFactor: 1,
		})
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close()) }()
	if err := app.Page.WaitLoad(); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, os.Remove(path))
		}
	}()

	stop := make(chan struct{})
	timer := time.AfterFunc(3*time.Second, func() { close(stop) })
	defer timer.Stop()
	if err := record(app.Page, stop, file); err != nil {
		return err
	}
	complete = true
	_, err = fmt.Fprintln(output, "Recorded:", path)
	return err
}
