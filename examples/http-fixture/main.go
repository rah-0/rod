// Run with: go run ./examples/http-fixture
// Serve application assets and API responses on a real local HTTP origin.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><h1 id="result">Loading</h1>
<script src="/app.js" defer></script>`)
	})
	mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, `window.configuredWidth = window.innerWidth;
fetch('/api/items').then(response => response.json()).then(items => {
    document.querySelector('#result').textContent = items[0].name;
    document.body.dataset.count = items.length;
    document.body.dataset.ready = 'true';
});`)
	})
	mux.HandleFunc("GET /api/items", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"name":"Notebook"},{"name":"Pencil"}]`)
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	app, err := fixture.New(browser, mux, func(page *rod.Page) error {
		// Configuration runs before the initial document and relative scripts.
		return page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
			Width: 720, Height: 480, DeviceScaleFactor: 1,
		})
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close()) }()

	// Navigation alone does not wait for the application's fetch to finish.
	if err := app.Page.Wait(rod.Eval(`() => document.body.dataset.ready === 'true'`)); err != nil {
		return err
	}
	var result struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
		Width int    `json:"width"`
	}
	if err := app.Page.EvalJSON(&result, `() => ({
    name: document.querySelector('#result').textContent,
    count: Number(document.body.dataset.count),
    width: window.configuredWidth
})`); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "HTTP app: %s (%d items, %dpx)\n", result.Name, result.Count, result.Width); err != nil {
		return err
	}

	// A single document needs no handler. Both fixtures share a caller-owned
	// browser; use separate incognito contexts when storage must be isolated.
	inline, err := fixture.HTML(browser, `<!doctype html><h1>Inline fixture</h1>`, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, inline.Close()) }()
	heading, err := inline.Page.Element("h1")
	if err != nil {
		return err
	}
	text, err := heading.Text()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "Inline page:", text)
	return err
}
