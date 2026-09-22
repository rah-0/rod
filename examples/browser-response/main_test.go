package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

type responseTestClient struct {
	*cdp.Client
	mu          sync.Mutex
	method      string
	failure     error
	after       bool
	before      func(context.Context)
	replacement []byte
	encodings   []bool
	commands    []string
}

func (c *responseTestClient) Call(ctx context.Context, session, method string, params any) ([]byte, error) {
	c.mu.Lock()
	if strings.HasPrefix(method, "Fetch.") {
		c.commands = append(c.commands, method)
	}
	var failure error
	var before func(context.Context)
	var replacement []byte
	after := false
	if c.method == method {
		failure, before, after, replacement = c.failure, c.before, c.after, c.replacement
		c.method = ""
	}
	c.mu.Unlock()
	if before != nil {
		before(ctx)
	}
	if failure != nil && !after {
		return nil, failure
	}
	data, err := c.Client.Call(ctx, session, method, params)
	if err != nil {
		return nil, err
	}
	if method == "Fetch.getResponseBody" {
		var body proto.FetchGetResponseBodyResult
		if err := json.Unmarshal(data, &body); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.encodings = append(c.encodings, body.Base64Encoded)
		c.mu.Unlock()
	}
	if replacement != nil {
		return replacement, nil
	}
	return data, failure
}

func newResponseBrowser(t *testing.T) (*rod.Browser, *responseTestClient) {
	t.Helper()
	// The attached browser and transport must remain alive during t.Cleanup.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	t.Cleanup(cancel)
	l := launcher.New().Headless(true).RemoteDebuggingPort(0)
	t.Cleanup(func() {
		l.Kill()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := l.CleanupContext(cleanup); err != nil {
			t.Error(err)
		}
	})
	endpoint, err := l.LaunchNew(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := cdp.StartWithURL(ctx, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	client := &responseTestClient{Client: transport}
	browser := rod.New().NoDefaultDevice().ControlURL("").Context(ctx).Client(client)
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := browser.CloseWithTimeout(5 * time.Second); err != nil {
			t.Error(err)
		}
	})
	return browser, client
}

func responseFixture(t *testing.T, browser *rod.Browser, handler http.Handler) *fixture.Fixture {
	t.Helper()
	app, err := fixture.New(browser, handler, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := app.Page.WaitLoad(); err != nil {
		t.Fatal(err)
	}
	return app
}

func responseHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	switch r.URL.Path {
	case "/":
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<!doctype html><title>Capture fixture</title>")
	case "/binary":
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0, 255, 1, 128})
	case "/redirect":
		http.Redirect(w, r, "/text", http.StatusFound)
	case "/bodyless", "/favicon.ico":
		w.WriteHeader(http.StatusNoContent)
	case "/not-modified":
		w.WriteHeader(http.StatusNotModified)
	case "/error":
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "unavailable")
	default:
		_, _ = io.WriteString(w, "response text")
	}
}

func assertResponsePageWorks(t *testing.T, page *rod.Page) {
	t.Helper()
	var state proto.FetchEnable
	if page.LoadState(&state) {
		t.Fatal("capture retained Fetch configuration")
	}
	ctx, cancel := context.WithTimeout(page.GetContext(), 3*time.Second)
	defer cancel()
	var text string
	if err := page.Context(ctx).EvalJSON(&text, `async () => (await fetch('/text')).text()`); err != nil || text != "response text" {
		t.Fatalf("subsequent page request = %q, %v", text, err)
	}
}

func TestBrowserResponseExample(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "POST requests: 1\nCaptured: authenticated response\nPage received: authenticated response\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

type responseBodyCase struct {
	path   string
	final  string
	method string
	status int
	body   []byte
}

func TestBrowserResponseBodies(t *testing.T) {
	browser, client := newResponseBrowser(t)
	app := responseFixture(t, browser, http.HandlerFunc(responseHandler))
	for _, test := range []responseBodyCase{
		{path: "/text", status: 200, body: []byte("response text")},
		{path: "/binary", status: 200, body: []byte{0, 255, 1, 128}},
		{path: "/redirect", final: "/text", status: 200, body: []byte("response text")},
		{path: "/bodyless", status: 204},
		{path: "/not-modified", status: 304},
		{path: "/head", method: "HEAD", status: 200},
		{path: "/error", status: 503, body: []byte("unavailable")},
	} {
		t.Run(test.path, func(t *testing.T) {
			if test.path == "/text" {
				// Chrome may encode even text as base64. Exercise the other CDP
				// wire representation while still continuing the real response.
				client.mu.Lock()
				client.method = "Fetch.getResponseBody"
				client.replacement = []byte(`{"body":"response text","base64Encoded":false}`)
				client.mu.Unlock()
			}
			url := strings.TrimSuffix(app.URL, "/") + test.path
			if test.final != "" {
				url = strings.TrimSuffix(app.URL, "/") + test.final
			}
			method := test.method
			if method == "" {
				method = "GET"
			}
			var pageBody []byte
			response, err := captureResponse(app.Page, url, func(page *rod.Page) error {
				return page.EvalJSON(&pageBody, `async (path, method) => {
					await fetch('/other');
					const response = await fetch(path, {method});
					return Array.from(new Uint8Array(await response.arrayBuffer()));
				}`, test.path, method)
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.Status != test.status || !bytes.Equal(response.Body, test.body) || !bytes.Equal(pageBody, test.body) {
				t.Fatalf("response=%+v, page body=%v; want status=%d body=%v", response, pageBody, test.status, test.body)
			}
			assertResponsePageWorks(t, app.Page)
		})
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.encodings) != 4 || !client.encodings[1] {
		t.Fatalf("expected four body reads including base64 binary, got %v", client.encodings)
	}
}

func TestBrowserResponseFailures(t *testing.T) {
	browser, client := newResponseBrowser(t)
	app := responseFixture(t, browser, http.HandlerFunc(responseHandler))
	for _, method := range []string{"Fetch.enable", "Fetch.getResponseBody", "Fetch.continueRequest", "invalid-base64"} {
		t.Run(method, func(t *testing.T) {
			failure := &cdp.Error{Code: -32000, Message: "injected protocol failure"}
			client.mu.Lock()
			client.method, client.failure, client.after = method, failure, method == "Fetch.enable"
			client.replacement = nil
			if method == "invalid-base64" {
				client.method, client.failure = "Fetch.getResponseBody", nil
				client.replacement = []byte(`{"body":"!","base64Encoded":true}`)
			}
			client.mu.Unlock()
			var triggered atomic.Bool
			_, err := captureResponse(app.Page, app.URL+"text", func(page *rod.Page) error {
				triggered.Store(true)
				var result string
				return page.EvalJSON(&result, `() => {
					window.lastResponse = fetch('/text').then(response => response.text());
					return window.lastResponse;
				}`)
			})
			if err == nil || method != "invalid-base64" && !errors.Is(err, failure) {
				t.Fatalf("capture error = %v", err)
			}
			if method == "Fetch.enable" && triggered.Load() {
				t.Fatal("setup failure ran the trigger")
			}
			assertResponsePageWorks(t, app.Page)
			if triggered.Load() {
				var text string
				if err := app.Page.EvalJSON(&text, `() => window.lastResponse`); err != nil || text != "response text" {
					t.Fatalf("original paused request = %q, %v", text, err)
				}
			}
		})
	}
}

func TestBrowserResponseCancellation(t *testing.T) {
	browser, client := newResponseBrowser(t)
	app := responseFixture(t, browser, http.HandlerFunc(responseHandler))
	for _, when := range []string{"before", "waiting", "reading"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(app.Page.GetContext())
			defer cancel(nil)
			cause := errors.New("stop response capture")
			if when == "before" {
				cancel(cause)
			}
			if when == "reading" {
				client.mu.Lock()
				client.method, client.failure, client.replacement = "Fetch.getResponseBody", nil, nil
				client.before = func(readCtx context.Context) {
					cancel(cause)
					if readCtx.Err() != nil {
						t.Error("caller cancellation interrupted the in-flight body read")
					}
				}
				client.mu.Unlock()
				defer func() {
					client.mu.Lock()
					client.before = nil
					client.mu.Unlock()
				}()
			}
			_, err := captureResponse(app.Page.Context(ctx), app.URL+"text", func(page *rod.Page) error {
				if when == "before" {
					return errors.New("unexpected trigger after cancellation")
				}
				if when == "waiting" {
					cancel(cause)
					return context.Cause(page.GetContext())
				}
				var text string
				return page.EvalJSON(&text, `async () => (await fetch('/text')).text()`)
			})
			if !errors.Is(err, cause) {
				t.Fatalf("capture cancellation = %v, want %v", err, cause)
			}
			assertResponsePageWorks(t, app.Page)
		})
	}
}

func TestBrowserResponseCacheAndServiceWorker(t *testing.T) {
	browser, _ := newResponseBrowser(t)
	version, err := (proto.BrowserGetVersion{}).Call(browser)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("response interception behavior: %s", version.Product)
	var cachedRequests, workerRequests atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cached":
			cachedRequests.Add(1)
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "cached response")
		case "/sw.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = io.WriteString(w, `
				self.addEventListener('install', event => event.waitUntil(self.skipWaiting()));
				self.addEventListener('activate', event => event.waitUntil(self.clients.claim()));
				self.addEventListener('fetch', event => {
					if (new URL(event.request.url).pathname === '/worker') {
						event.respondWith(new Response('service worker response'));
					}
				});`)
		case "/worker":
			workerRequests.Add(1)
			_, _ = io.WriteString(w, "server response")
		default:
			responseHandler(w, r)
		}
	})
	app := responseFixture(t, browser, handler)
	var pageBody string
	for range 2 {
		if err := app.Page.EvalJSON(&pageBody, `async () => (await fetch('/cached')).text()`); err != nil {
			t.Fatal(err)
		}
	}
	if cachedRequests.Load() != 1 {
		t.Fatalf("warm requests did not use cache: %d", cachedRequests.Load())
	}
	response, err := captureResponse(app.Page, app.URL+"cached", func(page *rod.Page) error {
		return page.EvalJSON(&pageBody, `async () => (await fetch('/cached')).text()`)
	})
	if err != nil || response == nil || string(response.Body) != "cached response" || cachedRequests.Load() != 1 {
		t.Fatalf("cache interception: response=%+v error=%v server requests=%d", response, err, cachedRequests.Load())
	}
	if err := app.Page.EvalJSON(nil, `async () => {
		await navigator.serviceWorker.register('/sw.js');
		await navigator.serviceWorker.ready;
		if (!navigator.serviceWorker.controller) {
			await new Promise(resolve => navigator.serviceWorker.addEventListener('controllerchange', resolve, {once: true}));
		}
	}`); err != nil {
		t.Fatal(err)
	}
	if err := app.Page.EvalJSON(&pageBody, `async () => (await fetch('/worker')).text()`); err != nil || pageBody != "service worker response" || workerRequests.Load() != 0 {
		t.Fatalf("service worker did not control the page: body=%q error=%v server requests=%d", pageBody, err, workerRequests.Load())
	}
	ctx, cancel := context.WithCancel(app.Page.GetContext())
	defer cancel()
	response, err = captureResponse(app.Page.Context(ctx), app.URL+"worker", func(page *rod.Page) error {
		err := page.EvalJSON(&pageBody, `async () => (await fetch('/worker')).text()`)
		cancel() // Completion without a paused event must not leave the test waiting.
		return err
	})
	if !errors.Is(err, context.Canceled) || response != nil || pageBody != "service worker response" || workerRequests.Load() != 0 {
		t.Fatalf("service-worker interception: captured=%+v error=%v page=%q server requests=%d", response, err, pageBody, workerRequests.Load())
	}
	if err := app.Page.EvalJSON(&pageBody, `async () => (await fetch('/worker')).text()`); err != nil || pageBody != "service worker response" || workerRequests.Load() != 0 {
		t.Fatalf("service worker did not recover after capture: body=%q error=%v server requests=%d", pageBody, err, workerRequests.Load())
	}
	assertResponsePageWorks(t, app.Page)
}

func TestBrowserResponseStalledBody(t *testing.T) {
	browser, client := newResponseBrowser(t)
	app := responseFixture(t, browser, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stalled" {
			responseHandler(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	_, err := captureResponse(app.Page, app.URL+"stalled", func(page *rod.Page) error {
		var body string
		return page.EvalJSON(&body, `async () => (await fetch('/stalled')).text()`)
	})
	if !errors.Is(err, errIncompleteBody) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled capture = %v", err)
	}
	client.mu.Lock()
	commands := append([]string(nil), client.commands...)
	client.mu.Unlock()
	if got := strings.Join(commands, ","); got != "Fetch.enable,Fetch.getResponseBody" {
		t.Fatalf("commands raced an incomplete body read: %s", got)
	}
	pages, err := browser.Pages()
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		if page.TargetID == app.Page.TargetID {
			t.Fatal("stalled capture kept its dedicated page open")
		}
	}
	replacement := responseFixture(t, browser, http.HandlerFunc(responseHandler))
	assertResponsePageWorks(t, replacement.Page)
}

func TestBrowserResponseAmbiguousRead(t *testing.T) {
	browser, client := newResponseBrowser(t)
	app := responseFixture(t, browser, http.HandlerFunc(responseHandler))
	client.mu.Lock()
	client.method, client.failure = "Fetch.getResponseBody", io.EOF
	client.mu.Unlock()
	_, err := captureResponse(app.Page, app.URL+"text", func(page *rod.Page) error {
		var body string
		return page.EvalJSON(&body, `async () => (await fetch('/text')).text()`)
	})
	if !errors.Is(err, errIncompleteBody) || !errors.Is(err, io.EOF) {
		t.Fatalf("ambiguous capture = %v", err)
	}
	client.mu.Lock()
	commands := strings.Join(client.commands, ",")
	client.mu.Unlock()
	if commands != "Fetch.enable,Fetch.getResponseBody" {
		t.Fatalf("commands raced an ambiguous body read: %s", commands)
	}
	pages, err := browser.Pages()
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		if page.TargetID == app.Page.TargetID {
			t.Fatal("ambiguous capture kept its dedicated page open")
		}
	}
}
