# HTTP fixture pages

See the [runnable example](../../examples/http-fixture/main.go).

`fixture.HTML` serves an HTML string on loopback HTTP and returns its page, URL,
and error-returning cleanup. `fixture.New` accepts an `http.Handler` for relative
resources and application endpoints. Both use a caller-owned connected browser
or incognito context:

```go
f, err := fixture.HTML(browser, `<button id="ready">Ready</button>`, nil)
if err != nil {
    t.Fatal(err)
}
t.Cleanup(func() {
    if err := f.Close(); err != nil {
        t.Error(err)
    }
})
f.Page.MustElement("#ready").MustClick()
```

`fixture.HTML(browser, html, configure)` serves UTF-8 HTML at `/` and
`/index.html`, disables caching with `Cache-Control: no-store`, returns 204 for
`/favicon.ico`, and returns 404 elsewhere. Empty HTML is valid.
`fixture.New(browser, handler, configure)` leaves routing and headers, including
the favicon, to its handler. A missing browser or nil handler, including a typed
nil, returns `fixture.ErrConfiguration` before allocating a page or server.

Both functions create a blank page, call the optional `func(*rod.Page) error`
configuration callback, then navigate to the server root. Set viewport options
and start diagnostics in that callback to capture initial scripts. The callback
must honor the page context and owns resources it creates; retain and stop a
diagnostics collector even if navigation later fails.

Treat `Fixture.Page` and `Fixture.URL` as read-only. Navigation returns after
response headers; use `Page.WaitLoad`, element waits, or application conditions
for readiness. Navigation or a repaint alone does not establish readiness.

`Fixture.Close` is idempotent and safe to call concurrently. Its five-second
budget is independent of expired operation contexts. It cancels handler requests,
closes server connections, and forcibly closes its page without beforeunload
dialogs. Setup failures perform the same cleanup. Stop diagnostics before
closing the fixture to obtain a complete final snapshot.

Handlers must honor request cancellation and release their own resources. Close
does not wait for arbitrary handler code; hijacked connections remain
handler-owned. Custom CDP clients must honor request cancellation. An interrupted
write on the built-in transport closes the connection because a partial frame
cannot safely be resumed.

The caller owns the browser and closes it after its fixtures. Use separate
`Browser.Incognito` contexts or profiles for storage isolation. Each server uses
an ephemeral port; different ports create different origins for local storage
and IndexedDB, while cookies are shared across ports on the same host within a
browser context.

The browser must run on the same host as the loopback server. For remote
browsers, arrange a reachable server and use `Page.Navigate`. Other pages and
external servers remain caller-owned. The package does not launch browsers or
choose test assertions. See [the tests](fixture_test.go) for routing, browser
storage, initial configuration, failure rollback, and cleanup coverage.
