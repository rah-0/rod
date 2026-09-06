// Run with: go run ./examples/network
// Configure request headers and cookies before the initial navigation.
package main

import (
	"context"
	"errors"
	"fmt"
	"html"
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

type RequestValues struct {
	Header string `json:"header"`
	Cookie string `json:"cookie"`
}

func run(ctx context.Context, output io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	browser := rod.New().ControlURL("").Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.CloseWithTimeout(5*time.Second)) }()

	initial := make(chan RequestValues, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		received := RequestValues{Header: r.Header.Get("X-Example")}
		if cookie, err := r.Cookie("session"); err == nil {
			received.Cookie = cookie.Value
		}
		select {
		case initial <- received:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><p id="header">%s</p><p id="cookie">%s</p>`,
			html.EscapeString(received.Header), html.EscapeString(received.Cookie))
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	app, err := fixture.New(browser, mux, func(page *rod.Page) error {
		// These headers remain configured for this owned page's lifetime.
		if _, err := page.SetExtraHeaders([]string{"X-Example", "configured"}); err != nil {
			return err
		}
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
	var received RequestValues
	select {
	case received = <-initial:
	case <-ctx.Done():
		return ctx.Err()
	}
	if received.Header != "configured" || received.Cookie != "example" {
		return fmt.Errorf("initial request missed configuration: %+v", received)
	}
	var displayed RequestValues
	if err := app.Page.EvalJSON(&displayed, `() => ({
		header: document.querySelector("#header").textContent,
		cookie: document.querySelector("#cookie").textContent
	})`); err != nil {
		return err
	}
	if displayed != received {
		return fmt.Errorf("page displayed %+v, server received %+v", displayed, received)
	}

	// DevTools can inspect HttpOnly cookies, while page JavaScript cannot.
	cookies, err := app.Page.Cookies([]string{app.URL})
	if err != nil {
		return err
	}
	var stored *proto.NetworkCookie
	for _, cookie := range cookies {
		if cookie.Name == "session" {
			stored = cookie
		}
	}
	if stored == nil || stored.Value != "example" || !stored.HTTPOnly {
		return fmt.Errorf("expected the configured HttpOnly browser cookie")
	}
	var scriptCookies string
	if err := app.Page.EvalJSON(&scriptCookies, `() => document.cookie`); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Initial request: X-Example=%s, session=%s\nBrowser cookie: %s=%s (HttpOnly=%t)\nJavaScript cookies: %q\n",
		received.Header, received.Cookie, stored.Name, stored.Value, stored.HTTPOnly, scriptCookies)
	return err
}
