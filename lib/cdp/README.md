# Overview

This client is directly based on this [doc](https://chromedevtools.github.io/devtools-protocol/).

You can treat it as a minimal example of how to use the DevTools Protocol, no complex abstraction.

It's thread-safe, and context first.

For basic usage, check this [file](example_test.go).

For more info, check the unit tests.

`WebSocket.Connect` uses its context only for connection establishment, including
TLS and the HTTP upgrade. Failed or canceled handshakes close the connection;
after success, the caller owns its lifetime and must call `Close` when finished.

`Client.Call` returns parameter-encoding errors. Invalid inbound JSON terminates
the reader, closes the event channel, and fails pending and subsequent calls
with the decoding error. After a transport read failure, subsequent calls also
return that terminal error without sending another request. These operational failures no longer panic. `Must*`
helpers continue to panic on errors.
