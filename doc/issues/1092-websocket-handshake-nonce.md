# Generate valid WebSocket handshake keys and honor header overrides

- Priority: P1
- Sources: [#1092](https://github.com/go-rod/rod/issues/1092#issuecomment-2787463658), proposed fix [PR #1228](https://github.com/go-rod/rod/pull/1228).
- Reviewed: 2026-09-05.
- Validation: reproduced against a local strict handshake fixture; the request sends `Sec-WebSocket-Key: nil`, fails base64 validation, and receives HTTP 400.

[WebSocket.handshake](../../lib/cdp/websocket.go) uses the literal `"nil"` instead of a base64-encoded random 16-byte nonce. Browser endpoints and proxies that validate the WebSocket handshake reject otherwise valid connections. Its override comparison is also case-sensitive: a header added with `http.Header.Set` is canonicalized to `Sec-Websocket-Key`, so the verification key can differ from the header actually sent.

Generate a fresh cryptographically random key for each handshake. Apply header names case-insensitively, retain one effective key, and verify `Sec-WebSocket-Accept` against exactly that key. Preserve the current cancellation, failed-connection cleanup, and standard protocol SHA-1 verification. The unrelated nonstandard CDP error-code request in #1092 is outside this task; malformed JSON returns a terminal error.

Acceptance criteria:

- Default handshake keys decode to 16 bytes and differ across connections.
- A local standards-checking WebSocket fixture accepts the default handshake.
- `http.Header.Set` and differently cased key overrides produce one effective header and successful matching accept verification.
- An incorrect accept value is rejected, and cancellation/connection-cleanup tests continue to pass.
