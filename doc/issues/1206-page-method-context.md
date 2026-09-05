# Honor page contexts in browser-routed page methods

Priority: P2. Source: [issue #1206](https://github.com/go-rod/rod/issues/1206).

Reviewed: 2026-09-05.

## Current evidence

[Page.Info and Page.Activate](../../page.go) call through `p.browser` without applying `p.ctx`. `TriggerFavicon` likewise performs its browser/headless preflight through the stored browser context. In contrast, other page helpers explicitly use `p.browser.Context(p.ctx)`.

Reproduced with a fake CDP client: `Info` and `Activate` on an already-canceled `Page.Context` clone both succeed, even though the client rejects requests actually carrying a canceled context. This is independent of cached-page reuse and occurs on a freshly created page.

## Scope

Route the affected browser-level requests through the calling page's context. Preserve browser-level session routing and each method's existing result semantics. Propagate headless-query operational failures normally instead of allowing a nil result to be dereferenced when that preflight is canceled.

## Acceptance

- A canceled/deadline-bound page governs Info, Activate, and TriggerFavicon's preflight requests.
- The original live page/browser remains usable after canceling a clone.
- Fake-client tests verify both context identity and routing, including headless-query failures.
