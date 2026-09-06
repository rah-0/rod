// Run with: go run ./examples/page-diagnostics
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strings"
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

	browser := rod.New().Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.Close()) }()

	var collector *rod.PageDiagnostics
	var page *fixture.Fixture
	// Keep this cleanup installed during fixture setup too. If navigation fails,
	// the fixture closes its page and Stop reports incomplete collection.
	defer func() {
		if collector != nil {
			_, stopErr := collector.Stop()
			err = errors.Join(err, stopErr)
		}
		if page != nil {
			err = errors.Join(err, page.Close())
		}
	}()

	page, err = fixture.HTML(browser, `<!doctype html>
<title>Page diagnostics</title>
<script>
console.log('initial script', 42);
window.resourcesReady = fetch('/missing').then(response => response.text());
</script>
<script>throw new Error('example exception');</script>`, func(page *rod.Page) error {
		// Configure runs before navigation, so initial inline scripts are observed.
		var err error
		collector, err = page.StartDiagnostics(rod.DiagnosticsOptions{})
		return err
	})
	if err != nil {
		return err
	}
	if err := page.Page.WaitLoad(); err != nil {
		return err
	}
	// Document load does not establish that a fetch has finished. Await the
	// fixture's own readiness promise before asking for a final event boundary.
	if _, err := page.Page.Eval(`async () => { await window.resourcesReady }`); err != nil {
		return err
	}
	report, stopErr := collector.Stop()
	collector = nil
	if stopErr != nil {
		return stopErr
	}

	// The structured records let callers choose what to report or treat as a
	// failure. Stop runs before fixture cleanup so the final snapshot can drain.
	var consoleSeen, exceptionSeen, resourceSeen bool
	for _, message := range report.Console {
		consoleSeen = consoleSeen || message.Text == "initial script 42"
		if _, err := fmt.Fprintf(output, "Console %s: %s\n", message.Type, message.Text); err != nil {
			return err
		}
	}
	for _, exception := range report.PageErrors {
		exceptionSeen = exceptionSeen || strings.Contains(exception.Text, "example exception")
		message, _, _ := strings.Cut(exception.Text, "\n")
		if _, err := fmt.Fprintf(output, "Unhandled exception: %s\n", message); err != nil {
			return err
		}
	}
	for _, failure := range report.ResourceFailures {
		location, err := url.Parse(failure.URL)
		if err != nil {
			return err
		}
		resourceSeen = resourceSeen || (location.Path == "/missing" && failure.Status == 404)
		if _, err := fmt.Fprintf(output, "HTTP failure: %s (%d)\n", location.Path, failure.Status); err != nil {
			return err
		}
	}
	if !consoleSeen || !exceptionSeen || !resourceSeen {
		return fmt.Errorf("missing fixture diagnostics: console=%t exception=%t HTTP failure=%t", consoleSeen, exceptionSeen, resourceSeen)
	}
	return nil
}
