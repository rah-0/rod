# Release CDP state retained after a page closes

- Priority: P1
- Sources: [#1226](https://github.com/go-rod/rod/issues/1226), related memory-growth report [#748](https://github.com/go-rod/rod/issues/748).
- Reviewed: 2026-09-05.
- Validation: reproduced with Chrome 152; after `SetDocumentContent` and successful `Page.Close`, `page.LoadState(&proto.PageSetDocumentContent{})` still returned `true`.

`Browser.Call` caches every successful command's parameters through `set` in [browser.go](../../browser.go) and [states.go](../../states.go). Keys include a session ID, so each new page leaves another set of entries. `Page.cleanupStates` removes only the target-to-page entry. Large document bodies and other command parameters remain reachable for the lifetime of the browser object.

Remove the closed page's session state when its lifecycle ends. Preserve browser-wide settings and states belonging to other pages or browser contexts. Account for target closure/detachment observed through CDP as well as an explicit `Page.Close`, and avoid reintroducing entries through in-flight calls after cleanup. Keep the existing public state APIs.

Acceptance criteria:

- Repeated create/set-document/close cycles leave no states for terminated sessions.
- Closing one page preserves another page's state and browser-wide settings.
- A page closed by the browser's target lifecycle also releases its cached state.
- Focused tests cover cleanup and concurrent call/close ordering without relying solely on heap-size thresholds.
