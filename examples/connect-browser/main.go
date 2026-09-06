// Run with: go run ./examples/connect-browser
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	// Stand in for an independently started local browser. The launcher keeps
	// process ownership when another Browser attaches to its control URL.
	l := launcher.New().Headless(true).RemoteDebuggingPort(0)
	defer func() {
		l.Kill()
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		err = errors.Join(err, l.CleanupContext(cleanup))
	}()
	controlURL, err := l.LaunchNew(ctx)
	if err != nil {
		return err
	}
	browser := rod.New().NoDefaultDevice().Context(ctx).ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, browser.CloseWithTimeout(5*time.Second))
		}
	}()

	page, err := fixture.HTML(browser, `<!doctype html><h1>Attached browser</h1>`, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, page.Close()) }()
	heading, err := page.Page.Element("h1")
	if err != nil {
		return err
	}
	text, err := heading.Text()
	if err != nil {
		return err
	}
	if text != "Attached browser" {
		return fmt.Errorf("unexpected page heading: %q", text)
	}
	// Finish page cleanup before closing its browser connection.
	if err := page.Close(); err != nil {
		return err
	}
	if err := browser.CloseWithTimeout(5 * time.Second); err != nil {
		return err
	}
	closed = true
	// Browser.close requests shutdown, but the caller must still establish
	// process exit and profile cleanup through the launcher it owns.
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if err := l.CleanupContext(cleanup); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Attached page: %s\nCaller-owned launcher: cleanup complete\n", text)
	return err
}
