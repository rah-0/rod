# Capture a browser response without replaying its request

Priority: P3 — useful optional example; start with the existing protocol API.

Sources: [PR #1053](https://github.com/go-rod/rod/pull/1053), [PR #698](https://github.com/go-rod/rod/pull/698), [PR #696](https://github.com/go-rod/rod/pull/696). Related metadata proposals: [PR #1183](https://github.com/go-rod/rod/pull/1183), [PR #1163](https://github.com/go-rod/rod/pull/1163).

## Why retain this task

The [hijack router](../../hijack.go) registers request-stage patterns, and `Hijack.LoadResponse` sends the intercepted request through a Go HTTP client. That flow does not observe the browser's response and can differ in authentication, proxy, and service-worker behavior. Capturing a transient fetch/XHR response while preserving browser-owned networking and single-use request semantics remains useful.

The [protocol package](../../lib/proto/fetch.go) exposes response-stage interception, `FetchGetResponseBody`, and `FetchContinueResponse`, but no focused example combines them safely. The old patches had unresolved cancellation/state leaks or lacked tests; redesign the flow.

## Scope

- Add an example capturing the next matching response on a dedicated page. Subscribe before enabling interception and triggering the request.
- Read and decode the body, resume paused requests, and return cancellation/body/continuation errors.
- Release the listener and Fetch configuration on every exit using bounded cleanup that survives cancellation.
- Document exclusive ownership of the page's Fetch configuration, including incompatibility with concurrent `HijackRequests`. Describe redirects, unreadable bodies, and service-worker/cache interception limits verified during implementation.
- Preserve existing APIs; use protocol calls without adding accessors or replaying requests.

## Acceptance checks

- A local POST endpoint receives exactly one request; capture and browser JavaScript receive the expected body.
- Cover text/binary bodies, redirects, bodyless/error responses, nonmatches, and browser cookie authentication.
- Cancellation before/during capture and setup/body/continuation failures leave subsequent page requests working, with no leaked listener or paused request. Run focused race tests.
