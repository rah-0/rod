# Handle WebSocket control frames before CDP decoding

Priority: P1. Source: [upstream PR #1187](https://github.com/go-rod/rod/pull/1187).

[`WebSocket.read`](../../lib/cdp/websocket.go) discards the first frame byte, including its opcode, and returns every payload as a CDP message. Close, Ping and Pong payloads therefore reach `Read` callers as ordinary data. [`Client.consumeMessages`](../../lib/cdp/client.go) reports malformed JSON cleanly, but a legitimate control frame can still terminate the CDP connection during decoding.

Handle control frames within the transport. [RFC 6455, section 5.5](https://datatracker.ietf.org/doc/html/rfc6455#section-5.5) defines their framing and response requirements. Reply to Ping with a matching Pong, consume Pong without emitting a CDP event, and process Close as connection termination with the required close response. Mask client control responses and preserve safe concurrent sending.

Do not apply the patch unchanged: it skips all three control opcodes, never answers Ping, and ignores Close. Peers can time out or leave callers waiting. The malformed-message suppression proposed in related [PR #1186](https://github.com/go-rod/rod/pull/1186) is unnecessary; retain the fork's terminal decoding-error behavior for malformed data messages.

Acceptance criteria:

- Ping followed by a valid CDP message returns only the CDP message and sends the matching Pong.
- Empty and nonempty Pong frames never reach JSON decoding.
- Close unblocks pending calls with a terminal transport error and completes connection cleanup.
- Invalid control framing is rejected without unbounded waits or a panic.
- Local frame-level tests and the existing CDP error/cancellation tests pass, including concurrent send coverage.
