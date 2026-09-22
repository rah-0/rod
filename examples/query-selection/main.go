// Run with: go run ./examples/query-selection
// Compare query choices and bound required waits on a local HTTP fixture.
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
	queryCtx, cancelQuery := context.WithTimeout(app.Page.GetContext(), 5*time.Second)
	defer cancelQuery()
	page := app.Page.Context(queryCtx)
	if err := page.WaitInteractive(); err != nil {
		return err
	}

	// Has returns immediately for an optional match, including absence.
	found, _, err := page.Has("#notice")
	if err != nil {
		return err
	}
	// Elements returns the current list; ElementR waits for a text match.
	items, err := page.Elements("#items li")
	if err != nil {
		return err
	}
	matched, err := page.ElementR("#items li", "^Beta$")
	if err != nil {
		return err
	}
	matchedText, err := matched.Text()
	if err != nil {
		return err
	}

	// Pass values as JavaScript arguments instead of inserting them into source.
	panel, err := page.ElementByJS(rod.Eval(`id => document.getElementById(id)`, "panel"))
	if err != nil {
		return err
	}
	// Element queries attempt once. The dot keeps XPath relative to panel.
	button, err := panel.ElementX("./button")
	if err != nil {
		return err
	}
	label, err := button.Text()
	if err != nil {
		return err
	}

	// Search owns a remote result. Release it even if reading the node fails.
	result, err := page.Search("#alpha")
	if err != nil {
		return err
	}
	searchText, readErr := result.First.Text()
	if err := errors.Join(readErr, result.Release()); err != nil {
		return err
	}

	// Enter the iframe document before querying its contents.
	iframe, err := page.Element("iframe")
	if err != nil {
		return err
	}
	frame, err := iframe.Frame()
	if err != nil {
		return err
	}
	if err := frame.WaitInteractive(); err != nil {
		return err
	}
	frameContent, err := frame.Element("#frame-content")
	if err != nil {
		return err
	}
	frameText, err := frameContent.Text()
	if err != nil {
		return err
	}

	// CSS selectors enter a shadow tree through the host's ShadowRoot.
	host, err := page.Element("#shadow-host")
	if err != nil {
		return err
	}
	shadow, err := host.ShadowRoot()
	if err != nil {
		return err
	}
	shadowContent, err := shadow.Element("p")
	if err != nil {
		return err
	}
	shadowText, err := shadowContent.Text()
	if err != nil {
		return err
	}

	if err := button.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}

	// Both outcomes share the five-second query deadline.
	winner, err := page.Race().Element("#failure").Element("#success").Do()
	if err != nil {
		return err
	}
	outcome, err := winner.Text()
	if err != nil {
		return err
	}

	// Start a separate deadline for a required element that never appears.
	waitCtx, cancelWait := context.WithTimeout(app.Page.GetContext(), 100*time.Millisecond)
	defer cancelWait()
	_, err = app.Page.Context(waitCtx).Element("#never-created")
	if !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("expected the missing element to time out, got %v", err)
	}
	_, err = fmt.Fprintf(output, `Optional notice: %t
CSS items: %d
Text match: %s
Scoped button: %s
Search result: %s
Frame text: %s
Shadow text: %s
Race outcome: %s
Missing element: deadline exceeded
`, found, len(items), matchedText, label, searchText, frameText, shadowText, outcome)
	return err
}
