# Implementation order

Follow this order for the retained pull-request tasks. It prioritizes crashes, hangs, and connection reliability, then functional fixes and optional examples. Each linked task contains its evidence, implementation constraints, and acceptance criteria.

**P1:** core correctness and resource handling. **P2:** functional fixes and platform support. **P3:** documentation and optional examples.

| Order | Priority | Task | Why here |
| --- | --- | --- | --- |
| 1 | P1 | [#1240 — Helper cache after context reset](1240-helper-cache-reset.md) | Prevents a panic during concurrent evaluation and navigation; a focused fix with deterministic coverage. |
| 2 | P1 | [#1241 — Repaint cancellation](1241-repaint-context.md) | Restores deadlines for repaint and element stability waits, preventing automation from hanging. Includes #1225 and #1219. |
| 3 | P1 | [#1221 — Cleanup after browser reuse](1221-launcher-reuse-cleanup.md) | Removes an indefinite shutdown wait while preserving browser and profile ownership. |
| 4 | P1 | [#1228 — Valid WebSocket handshake key](1228-valid-websocket-handshake-key.md) | Makes connections work with strict WebSocket servers and fixes header overrides. Includes #1210. |
| 5 | P1 | [#1187 — WebSocket control frames](1187-handle-websocket-control-frames.md) | Keeps valid Ping/Pong traffic from terminating CDP sessions and handles Close correctly. Follows the handshake work in the same transport. |
| 6 | P1 | [#1235 — Release closed-page session state](1235-release-closed-page-session-state.md) | Stops cached request data accumulating in long-running browser sessions. |
| 7 | P1 | [#1197 — Explicit false protocol options](1197-explicit-false-protocol-options.md) | Restores missing protocol behavior; requires a compatibility decision before changing generated public fields. |
| 8 | P2 | [#983 — Return hijack pattern errors](983-return-hijack-pattern-errors.md) | Removes a caller-triggered panic and partial registration with a small, local correction. |
| 9 | P2 | [#1150 — WaitLoad event result](1150-wait-load-event-result.md) | Fixes load waits failing on circular event objects; update the JS source and generated helper together. |
| 10 | P2 | [#1128 — Replay hijacked request bodies](1128-replay-hijacked-request-bodies.md) | Restores body-preserving redirects for forwarded requests without buffering arbitrary streams. |
| 11 | P2 | [#1200 — Element screenshot scale](1200-element-screenshot-scale.md) | Corrects screenshot content and dimensions; needs broader browser coverage for scale, scrolling, and frames. |
| 12 | P2 | [#1220 — Unicode key printability](1220-unicode-key-printability.md) | Corrects text events for registered non-ASCII keys while keeping the scope limited to printability. |
| 13 | P2 | [#1233 — FreeBSD browser discovery](1233-freebsd-browser-discovery.md) | Adds a small platform improvement; runtime verification requires FreeBSD. |
| 14 | P3 | [#1182 — Wait-state documentation links](1182-wait-state-doc-links.md) | Corrects two misleading references; inexpensive enough to pick up alongside earlier work. |
| 15 | P3 | [#1053 — Capture the browser's response](1053-response-stage-capture.md) | Adds an optional example using existing protocol calls, after core lifecycle and transport fixes. |
| 16 | P3 | [#1152 — Screencast recording example](1152-screencast-example.md) | Adds an optional debugging aid with more output, timing, and cleanup complexity. |

Start the compatibility assessment for **#1197** alongside the first six fixes. If its implementation requires changing exported `bool` fields to `*bool`, schedule that change for an intentional breaking release and continue with the remaining tasks. Preserve existing request defaults and the standard `encoding/json` contract.

Most tasks are independent and can proceed in parallel when their edits do not overlap. Sequence #1228 and #1187 together because they share the WebSocket implementation and tests. The response-capture example in #1053 uses browser-owned networking and has no dependency on the Go HTTP replay fix in #1128. Keep both optional examples behind the core fixes, and apply each task's acceptance checks before marking it complete.
