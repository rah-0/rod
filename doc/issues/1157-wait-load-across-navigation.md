# Keep the document-load wait alive across navigation context replacement

Priority: P2 · Correctness · Validated by static inspection on 2026-09-05; the upstream website was not reproduced.

Sources: [upstream issue #1157](https://github.com/go-rod/rod/issues/1157), earlier redirect report [#640](https://github.com/go-rod/rod/issues/640).

A load wait started while a redirect or script-driven navigation replaces the JavaScript execution context can fail with `Execution context was destroyed` instead of waiting for the new document. The issue reports this through `MustWaitStable`, which calls `WaitLoad` internally.

[Page.WaitLoad](../../page.go) evaluates the promise-based load helper once. [Page.Evaluate](../../page_eval.go) retries `cdp.ErrCtxNotFound`, but returns the distinct [cdp.ErrCtxDestroyed](../../lib/cdp/error.go) immediately. That matches the reported error path in the current implementation. This does not establish that every `WaitStable` failure has the same cause.

Make the library's read-only document-load wait re-establish itself after a navigation destroys its execution context, with cancellation and page/session closure respected. Keep retry behavior local to the load-wait operation: retrying arbitrary user JavaScript after it may already have run can duplicate side effects. This task is separate from fixing the load listener's immediate readiness check or resource cleanup.

Acceptance criteria:

- A deterministic local fixture starts waiting on one document, navigates to another before load, and returns only when the replacement document loads.
- Cover both an HTTP redirect chain and script-driven navigation with a stalled subresource.
- Caller cancellation, an expired timeout and page closure terminate the wait without a retry loop or leaked listeners.
- An already-loaded document still returns immediately and unrelated evaluation errors are preserved.
- Exercise `WaitLoad` directly and its use through `WaitStable`; do not change generic evaluation retry semantics.
