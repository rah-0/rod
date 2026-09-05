# Add an example for capturing browser-owned responses

Priority: P3, bounded feature/example. Sources: [issue #607](https://github.com/go-rod/rod/issues/607), [#693](https://github.com/go-rod/rod/issues/693), [#466](https://github.com/go-rod/rod/issues/466). Related proposals: [PR #1053](https://github.com/go-rod/rod/pull/1053), [#698](https://github.com/go-rod/rod/pull/698), [#696](https://github.com/go-rod/rod/pull/696). See the existing [pull assessment](../pulls/1053-response-stage-capture.md).

Reviewed: 2026-09-05.

## Current evidence

[HijackRouter.Add](../../hijack.go) enables request-stage interception, while `Hijack.LoadResponse` sends a separate request through `http.Client.Do`. This cannot preserve the browser's original networking identity and can replay an operation with single-use semantics. [FetchGetResponseBody](../../lib/proto/fetch.go) already supports reading the response of a request paused in the response stage, but there is no focused safe example combining setup, capture, continuation, and cleanup. `Page.GetResource` is not a general XHR response archive.

Assessment: current APIs and implementation verified statically; response capture has not been validated in a browser.

## Scope

Add a documented example that captures the next matching response on a dedicated page using the existing Fetch protocol API. Subscribe before enabling interception and triggering the request. Decode text/base64 bodies, resume all paused requests, and release owned listeners/configuration on every exit with cleanup that survives operation cancellation. Document exclusive Fetch ownership and verified redirect/service-worker/cache limitations. Preserve the current public router API.

## Acceptance

- A local POST endpoint receives exactly one request; both capture and page JavaScript receive its response.
- Cover browser cookie authentication, text/binary bodies, redirects, bodyless responses, and nonmatching traffic.
- Setup/read/continue failures and cancellation leave subsequent requests functional without leaked listeners or paused requests.
