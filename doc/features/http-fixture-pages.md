# Open HTML fixtures on a real HTTP origin

Priority: P3. Status: proposed; optional test support.

## Value and current support

Small browser fixtures often begin as an HTML string, then grow to require
relative scripts, fetch endpoints, cookies, or browser storage. Serving them
over loopback HTTP provides normal navigation and origin behavior, while callers
repeatedly wire together a server, a blank page, navigation, and cleanup.

[Page.SetDocumentContent](../../page.go) already replaces the current document;
it does not create an HTTP origin or serve relative resources. Go's
`httptest.NewServer` already serves handlers. Rod's
[basic examples](../../examples_test.go) use it directly, and the
[e2e setup](../../lib/examples/e2e-testing/setup_test.go) demonstrates browser
context isolation using data URLs. The missing convenience is their reusable
composition with explicit ownership, not an HTTP server implementation.

## Proposed behavior

Provide a small optional fixture helper outside the core `rod` package. Accept
an existing browser or browser context so this helper does not also choose an
executable, launch a process, or impose an isolation strategy.

Support two fixture inputs: an HTML string and a caller-provided `http.Handler`.
Create a server bound to loopback on an ephemeral port, create a page, allow page
configuration before navigation, and expose the page, URL, and error-returning
cleanup. Initial viewport changes and diagnostic subscriptions must be possible
before fixture scripts run.

For HTML input, serve UTF-8 HTML at `/` and `/index.html`, disable caching, return
an empty successful favicon response, and return 404 for unknown paths. Handler
input owns its routing and headers, including favicon behavior. An empty HTML
string is a valid document; a nil handler is a configuration error.

Keep readiness explicit: document the navigation step and use existing load,
repaint, and application-condition waits. Do not imply that navigation or one
repaint establishes application readiness. Existing external URLs continue to
use `Page.Navigate` and are never owned by the fixture helper.

Cleanup closes only the page and server created for this fixture, including
partial setup failures. It must be idempotent, survive an expired operation
context, and have a finite shutdown budget. Active handlers must not leave an
unbounded `httptest.Server.Close` wait; document handler cancellation and force
connection closure when required. Handlers must honor request cancellation;
the helper cannot forcibly stop arbitrary handler code. Callers own the supplied
browser/context and the resources used internally by their handler.

An ephemeral port creates a distinct web origin, but does not isolate host
cookies across ports. Use separate browser contexts or profiles when cookie or
broader browser-state isolation is required. A loopback server is reachable by
a browser on the same host; remote browsers need caller-arranged reachability
and remain outside this helper's initial scope.

Keep this feature small. It should replace repetitive fixture setup, with a
focused example showing normal Rod operations and `testing.T` cleanup. It should
not introduce routing abstractions, an action interface, assertions, automatic
test failure policy, or a general browser runner.

## Acceptance

- A served HTML page can use local storage and IndexedDB, load a relative script,
  and fetch a handler-provided endpoint in the applicable fixture mode.
- Verify content type, cache policy, favicon handling, unknown routes, empty
  HTML, and rejection of a nil handler before page/server allocation.
- Page configuration and diagnostics can be installed before the first script
  executes. Application readiness remains an explicit caller wait.
- Parallel fixtures have independent servers and pages. Demonstrate storage
  isolation using separate browser contexts, including cookies at the same host.
- Page creation/navigation failure and cancellation release owned resources.
  Repeated cleanup is harmless, and a slow, cancellation-aware handler does not
  exceed the cleanup budget or leave an untracked server shutdown waiter.
- Closing a fixture leaves the supplied browser/context and external services
  usable. Examples show the caller's corresponding browser/context cleanup.
