// Run with: go run ./examples/browser-response
// Capture the browser's authenticated POST response without replaying the request.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync/atomic"
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
	browser := rod.New().ControlURL("").Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.CloseWithTimeout(5*time.Second)) }()

	var requests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<!doctype html><title>Browser response capture</title>")
	})
	mux.HandleFunc("POST /once", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value != "example" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "one operation" {
			http.Error(w, "unexpected request body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "authenticated response")
	})
	app, err := fixture.New(browser, mux, func(page *rod.Page) error {
		return page.SetCookies([]*proto.NetworkCookieParam{{
			Name: "session", Value: "example", Domain: "127.0.0.1", Path: "/", HTTPOnly: true,
		}})
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close()) }()
	if err := app.Page.WaitLoad(); err != nil {
		return err
	}
	var pageBody string
	response, err := captureResponse(app.Page, app.URL+"once", func(page *rod.Page) error {
		return page.EvalJSON(&pageBody, `async () => {
			const response = await fetch('/once', {method: 'POST', body: 'one operation'});
			return response.text();
		}`)
	})
	if err != nil {
		return err
	}
	if requests.Load() != 1 || response.Status != http.StatusOK ||
		string(response.Body) != "authenticated response" || pageBody != string(response.Body) {
		return fmt.Errorf("response mismatch: requests=%d captured=%q page=%q", requests.Load(), response.Body, pageBody)
	}
	_, err = fmt.Fprintf(output, "POST requests: %d\nCaptured: %s\nPage received: %s\n", requests.Load(), response.Body, pageBody)
	return err
}
