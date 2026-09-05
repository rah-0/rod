# Restrict incognito page enumeration to its browser context

Priority: P2 · Correctness · Confirmed with a synthetic CDP client on 2026-09-05.

Source: [upstream issue #437](https://github.com/go-rod/rod/issues/437), closed without an implementation explanation.

Calling `Pages()` on an incognito `Browser` returns pages from other contexts. Code that enumerates and closes or navigates that incognito browser's pages can therefore act on unrelated sessions.

[Browser.Incognito](../../browser.go) records `BrowserContextID`, but [Browser.Pages](../../browser.go) obtains every target and filters only by target type before calling `PageFromTarget`. `TargetTargetInfo.BrowserContextID` is available for filtering. This behavior remains in the implementation despite the upstream issue being closed.

A browser-free reproduction supplied page targets from contexts A, B and the default context. A `Browser` with `BrowserContextID == "A"` returned all three target IDs. No actual browser was used for this check.

Filter target records before attachment when the receiver has a nonempty `BrowserContextID`. Preserve the root browser's existing ability to enumerate all visible pages; document that distinction. Do not change `PageFromTarget` into an authorization boundary or alter browser context creation.

Acceptance criteria:

- Two incognito contexts each return only their own pages, excluding the default context and non-page targets.
- The root browser retains its existing enumeration behavior.
- Excluded targets are never attached or returned through the page cache.
- Closing pages enumerated from one incognito context leaves the other context's pages usable.
- Cover mocked target filtering and a local browser fixture with isolated contexts.
