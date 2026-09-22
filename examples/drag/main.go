// Run with: go run ./examples/drag
// Deliver a native HTML drag payload to a drop target on a local page.
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

//go:embed page.html
var pageHTML string

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	browser := rod.New().Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.Close()) }()

	app, err := fixture.HTML(browser, pageHTML, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close()) }()
	if err := app.Page.WaitLoad(); err != nil {
		return err
	}

	dragCtx, cancelDrag := context.WithTimeout(app.Page.GetContext(), 5*time.Second)
	defer cancelDrag()
	page := app.Page.Context(dragCtx)
	source, err := page.Element("#source")
	if err != nil {
		return err
	}
	start, err := source.Interactable()
	if err != nil {
		return err
	}
	target, err := page.Element("#target")
	if err != nil {
		return err
	}
	end, err := target.Interactable()
	if err != nil {
		return err
	}

	// Supply the payload explicitly: Page.Drag does not fire source dragstart.
	const payload = "hello from Rod"
	drag, err := page.Drag(*start, &proto.InputDragData{
		Items:              []*proto.InputDragDataItem{{MIMEType: "text/plain", Data: payload}},
		DragOperationsMask: 1, // Copy. Link is 2; move is 16.
	})
	if err != nil {
		return err
	}
	// Cancel also cleans up early returns; after Drop it is a no-op.
	defer func() { err = errors.Join(err, drag.Cancel()) }()
	if err := drag.MoveTo(*end); err != nil {
		return err
	}
	if err := drag.Drop(); err != nil {
		return err
	}

	received, err := target.Text()
	if err != nil {
		return err
	}
	if received != payload {
		return fmt.Errorf("drop target received %q, want %q", received, payload)
	}
	_, err = fmt.Fprintln(output, "Dropped:", received)
	return err
}
