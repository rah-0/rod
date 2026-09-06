# Overview

This client is directly based on this [doc](https://chromedevtools.github.io/devtools-protocol/).

You can treat it as a minimal example of how to use the DevTools Protocol, no complex abstraction.

It's thread-safe, and context first.

For basic usage, check this [file](example_test.go).

For more info, check the unit tests.

`WebSocket.Connect` uses its context only for connection establishment, including
TLS and the HTTP upgrade. Failed or canceled handshakes close the connection;
after success, the caller owns its lifetime and must call `Close` when finished.

`Client.Call` uses `WebSocket.SendContext` to honor cancellation while waiting
to write or sending a frame. Cancellation before a write leaves the connection
usable; an interrupted write closes it because a partial frame cannot safely be
resumed. Custom transports can implement `SendContext(context.Context, []byte)
error` for the same behavior; transports that implement only `Send` remain
responsible for bounding that operation. `Client.Close` closes an underlying
transport that implements `io.Closer`.

`Client.Close` also releases pending calls and a reader waiting to deliver an
unconsumed event. Those calls return `ErrClientClosed`; the event channel closes
when the reader exits. Repeated closes invoke the transport's `Close` only once.
A custom transport's `Close` must interrupt its blocked reads and writes.

The built-in transport generates a fresh handshake nonce and frame masks. Header
overrides are case insensitive; a custom `Sec-WebSocket-Key` must encode exactly
16 bytes, and conflicting duplicate headers are rejected. Plain WebSocket URLs
default to port 80 and secure URLs to port 443. TLS uses normal certificate
verification.

`Read` reassembles fragmented text, responds to ping, ignores pong, and returns
`ErrWebSocketClosed` with a `WebSocketCloseError` on peer close. Protocol replies
have a five-second write deadline. Invalid frames terminate the connection with
an error; binary messages and compression are unsupported. `Send` preserves its
input bytes.

`Client.Call` returns parameter-encoding errors. Invalid inbound JSON terminates
the reader, closes the event channel, and fails pending and subsequent calls
with the decoding error. After a transport read failure, subsequent calls also
return that terminal error without sending another request. These operational failures no longer panic. `Must*`
helpers continue to panic on errors.
