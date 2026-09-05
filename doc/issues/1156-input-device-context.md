# Propagate cloned page contexts to input devices

Priority: P1. Sources: [issue #1156](https://github.com/go-rod/rod/issues/1156), [#1206](https://github.com/go-rod/rod/issues/1206).

Reviewed: 2026-09-05.

## Current evidence

[Page.Context](../../context.go) shallow-copies the page. Its `Keyboard`, `Mouse`, and `Touch` pointers still hold the original `page` back-pointer in [input.go](../../input.go), and their CDP calls use that original context. A timeout or cancellation on `page.Context(ctx).Mouse` therefore does not govern input work.

Reproduced with a fake CDP client that rejects canceled contexts: on a page clone whose context is already canceled, both `Mouse.Scroll` and `Keyboard.Press` succeed because they use the live original context. This proves context loss; the upstream browser freeze was not reproduced.

## Scope

Bind input operations obtained from a page view to that view's context while keeping pressed keys/buttons, cursor position, tracing identity, and synchronization shared across views of the same session. Do not copy embedded used mutexes or create divergent input-state copies. Cover the `KeyActions` and element interaction paths that obtain page input devices.

## Acceptance

- Keyboard, mouse, touch, and composed key actions honor page and element operation cancellation/deadlines.
- Canceling one input view leaves another live view usable.
- Mixed operations from clones share modifier/button/cursor state safely under race tests.
- A stalled CDP input request returns promptly when its operation context is canceled.
