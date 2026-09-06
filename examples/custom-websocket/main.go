// Run with: go run ./examples/custom-websocket
// This separate module keeps gobwas/ws out of Rod's core dependencies.
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
	"github.com/rah-0/rod/lib/cdp"
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
	// Supplying a custom Client keeps process ownership with this launcher.
	l := launcher.New().Headless(true).RemoteDebuggingPort(0)
	defer func() {
		l.Kill()
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		err = errors.Join(err, l.CleanupContext(cleanup))
	}()
	endpoint, err := l.LaunchNew(ctx)
	if err != nil {
		return err
	}
	transport, err := NewWebSocket(ctx, endpoint)
	if err != nil {
		return err
	}
	client := cdp.New().Start(transport)
	defer func() { err = errors.Join(err, client.Close()) }()
	browser := rod.New().NoDefaultDevice().ControlURL("").Context(ctx).Client(client)
	if err := browser.Connect(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.CloseWithTimeout(5*time.Second)) }()
	page, err := fixture.HTML(browser, `<!doctype html><h1>Custom transport</h1>`, nil)
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
	if text != "Custom transport" {
		return fmt.Errorf("unexpected page heading: %q", text)
	}
	_, err = fmt.Fprintf(output, "Custom WebSocket page: %s\nTransport: gobwas/ws\n", text)
	return err
}
