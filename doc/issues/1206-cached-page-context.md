# Separate cached target sessions from caller page contexts

Priority: P1. Source: [issue #1206](https://github.com/go-rod/rod/issues/1206).

Reviewed: 2026-09-05.

## Current evidence

[Browser.PageFromTarget](../../browser.go) caches a complete `Page` created with the first caller's browser and context. Later calls return that pointer unchanged, even when invoked through a fresh `Browser.Context` clone. [cachePage/loadCachedPage](../../states.go) share this cache across browser clones.

Reproduced using a fake CDP client: create a cached page through a cancellable browser clone, cancel that context, then request the same target from the original live browser. The second lookup returns the identical page pointer with `GetContext().Err() == context.Canceled`. Its event subscription was also tied to the canceled context. An earlier caller can therefore poison subsequent use of a still-live browser target.

## Scope

Keep target/session ownership and shared input/cache state independent from transient operation contexts. Return a caller-bound page view without mutating another caller's view or copying used locks. Rebind page/browser references consistently, and keep session-detach handling alive for the session lifetime. Coordinate with the [input-context task](1156-input-device-context.md); simply changing `Page.ctx` leaves input-device back-pointers stale.

## Acceptance

- Canceling one lookup's operation context does not poison a later lookup of the same live target under another context.
- Concurrent callers retain independent cancellation while sharing one target session and synchronized input state.
- Target detach/browser shutdown still cancel all relevant views and remove stale session state.
- Deterministic fake-client tests and focused race tests cover reuse, cancellation, and detach without browser startup.
