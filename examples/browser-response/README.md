# Capture a browser response

Run from the repository root:

```sh
go run ./examples/browser-response
```

The example serves a local endpoint that requires a browser cookie and a POST
body. It captures the response through CDP Fetch while the page's JavaScript
receives the same body. The endpoint receives exactly one POST; no Go HTTP client
replays it. The output is:

```text
POST requests: 1
Captured: authenticated response
Page received: authenticated response
```

`captureResponse` is an example-local helper for a dedicated page. It subscribes
before enabling response-stage interception, then runs the supplied trigger
concurrently. Match the final response's exact URL. Ordinary capture continues
paused requests, including unrelated requests, redirects, and responses whose
bodies cannot be decoded. Text and base64 protocol bodies both become bytes. HTTP error
statuses retain their response bodies; HEAD, 204, 205, and 304 return empty bodies.
Redirect hops are continued without reading their unavailable bodies.

The helper requires exclusive ownership of the page's Fetch configuration. Do
not use it concurrently with `HijackRequests`, authentication handlers, or other
Fetch interception. It disables Fetch on ordinary completion or failure, cancels
and drains its event subscription, and joins the trigger goroutine. The trigger
must honor the supplied page context. Give the capture page an operation deadline
so a response that never matches cannot leave the caller waiting indefinitely.

Cleanup uses a separate five-second budget. If the caller cancels during a body
read, that read may finish within its own five-second budget before the paused
request is continued. A completed CDP error or invalid base64 body is reported
while the page remains usable. A body-read timeout or ambiguous transport error
instead closes the dedicated page and reports the original error and any close
error. Create a new page after that failure. This avoids continuing requests or
disabling Fetch while Chrome may still be reading the body, which the
[Fetch protocol](https://chromedevtools.github.io/devtools-protocol/tot/Fetch/#method-getResponseBody)
defines as undefined behavior. If the transport also prevents target closure,
the caller must dispose of the owning browser.

This buffers the entire body and is intended for small, finite responses, not
streaming or unbounded downloads. Interception can affect request timing and is
not a general archive of previous fetch/XHR responses.

The browser tests verified these limits with Chrome `152.0.7977.64` on Linux:

- A warm HTTP-cached response is captured without a second origin request.
  This does not establish identical behavior for every cache layer or browser.
- A synthetic service-worker response reaches page JavaScript without a
  page-session Fetch pause. It cannot be captured by this helper. The example
  does not bypass service workers or replace their responses with network data.
- Redirect bodies are not read; the final matching response is captured.

Tests also cover cookie authentication, a single POST, binary responses,
nonmatching traffic, bodyless and HTTP-error responses, setup/read/continuation
errors, caller cancellation, and closing a page whose response body stalls.
