// Run from the repository root with: go run ./examples/load-extension
// Load an unpacked Manifest V3 extension and observe its content script on a local page.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

func main() {
	if err := run(context.Background(), os.Stdout, "fixtures/chrome-extension"); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer, extensionPath string) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	// Extensions.loadUnpacked requires an absolute directory path.
	extensionPath, err = filepath.Abs(extensionPath)
	if err != nil {
		return err
	}
	browser := rod.New().ControlURL("").Context(ctx).NoDefaultDevice()
	l := launcher.New().Headless(true)
	if err := browser.Launch(l); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.CloseWithTimeout(5*time.Second)) }()

	if _, err := (proto.ExtensionsLoadUnpacked{Path: extensionPath}).Call(browser); err != nil {
		return fmt.Errorf("load extension: %w", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><title>Extension example</title><h1>Local page</h1>`)
	}))
	defer server.Close()

	page, err := browser.Page(proto.TargetCreateTarget{URL: server.URL})
	if err != nil {
		return err
	}
	// The page itself has no script. Only the extension can change its title.
	if err := page.Wait(rod.Eval(`() => document.title === 'test-extension'`)); err != nil {
		return fmt.Errorf("wait for extension content script: %w", err)
	}
	title, err := page.Eval(`() => document.title`)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Extension title: %s\n", title.Value.String())
	return err
}
