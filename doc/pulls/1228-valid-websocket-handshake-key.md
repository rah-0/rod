# Send a valid WebSocket handshake key

Priority: P1. Source: [upstream PR #1228](https://github.com/go-rod/rod/pull/1228); overlaps the closed [PR #1210](https://github.com/go-rod/rod/pull/1210).

[`WebSocket.handshake`](../../lib/cdp/websocket.go) still sends the literal `nil` as `Sec-WebSocket-Key`. Servers validating the opening handshake can reject it: [RFC 6455, section 4.1](https://datatracker.ietf.org/doc/html/rfc6455#section-4.1) requires a randomly selected 16-byte nonce encoded in base64.

Inspecting the HTTP request bytes confirms the default key is `nil`. Supplying an override through `http.Header.Set` produces two key values because Go canonicalizes the header name to `Sec-Websocket-Key`, while the implementation stores and compares `Sec-WebSocket-Key` literally. This also leaves response verification using the wrong key.

Generate the nonce with the standard library and normalize header insertion and overrides so exactly one effective key is sent. Use that same value when validating `Sec-WebSocket-Accept`. Merely adopting the PR's case-insensitive comparison still permits duplicate differently cased map entries. Its added test generates independent random bytes rather than asserting the nonce sent by the implementation, so replace that test approach.

Acceptance criteria:

- A strict local handshake server accepts the default connection and observes a base64 key decoding to 16 bytes.
- Independent connections receive independently generated keys.
- Canonical and differently cased caller overrides produce one key header and verify the corresponding accept value.
- Invalid accept responses still fail; existing cancellation and connection tests pass.
- Preserve `Browser.Connect()` and the existing lower-level header-capable connection APIs.
