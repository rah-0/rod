# Preserve replayable bodies when forwarding hijacked requests

Priority: P2. Source: [upstream PR #1128](https://github.com/go-rod/rod/pull/1128).

[`HijackRouter.new`](../../hijack.go) constructs `http.Request.Body` manually from captured post data. `HijackRequest.SetBody` replaces that reader but does not set `GetBody`. `Hijack.LoadResponse` then passes the request directly to `http.Client.Do`.

A reproduction using a local HTTP server demonstrated the failure for both 307 and 308 responses: a hijacked POST stopped at the redirect response instead of reaching the destination with its body. Go's [HTTP client redirect handling](https://go.dev/src/net/http/client.go) requires a replayable body for these body-preserving redirects. The PR also reports HTTP/2 retry failures; that behavior still needs separate verification.

Make captured and explicitly replaced bodies replayable when Rod creates them. Keep `Body`, `GetBody` and the known content length consistent. Prefer the standard request constructors and readers where practical, and preserve an explicitly supplied replay function. The upstream patch buffers every unknown body inside `LoadResponse`; avoid imposing that extra buffering on arbitrary streams exposed through `Req()` when Rod already has the original bytes.

Acceptance criteria:

- Original captured POST data and data supplied through `SetBody` survive 307 and 308 redirects unchanged.
- String, byte-slice and JSON bodies return independent readers containing the latest body.
- Replacing or clearing the body updates replay metadata without retaining stale content.
- Existing custom `GetBody` behavior and the public `LoadResponse(client, bool)` signature remain compatible.
- Tests distinguish verified redirect behavior from any separately reproduced HTTP/2 retry behavior.
