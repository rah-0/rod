# Release cached CDP state when a page closes

Priority: P1. Source: [upstream PR #1235](https://github.com/go-rod/rod/pull/1235).

[`Browser.Call`](../../browser.go) records successful command parameters through [`Browser.set`](../../states.go). These entries use a key containing the browser context, session ID and method. `Page.cleanupStates` removes only the cached page keyed by target ID. Consequently, closing pages retains session parameters, including complete HTML strings passed to `SetDocumentContent`, for the lifetime of the browser.

To reproduce without a browser, cache a page and `Page.setDocumentContent` parameters, invoke `cleanupStates`, then read the HTML through `Page.LoadState`. The cached page is removed, but its command parameters remain readable. Repeated page creation therefore accumulates retained session data; this is separate from the existing page-cache removal.

Extend successful page cleanup to delete the closing session's cached command state. Preserve other pages, browser-level state and context isolation. The upstream map scan is a reasonable starting point, but verify how concurrent calls and the existing page-close lifecycle affect cleanup. Keep the change within the existing state machinery.

Acceptance criteria:

- Cleanup removes the target cache entry and every command state belonging to its session, including large document content.
- Other sessions and browser-level state remain readable.
- Repeated creation, state storage and cleanup do not increase the number of retained session entries.
- Repeated cleanup is harmless, and a canceled page close preserves the live page's state.
- Focused state tests pass; lifecycle tests cover the successful close path.
