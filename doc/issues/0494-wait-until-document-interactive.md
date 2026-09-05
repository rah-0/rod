# Wait for document parsing without waiting for every subresource

Priority: P3 · Bounded enhancement · Confirmed API gap by static inspection on 2026-09-05.

Sources: [upstream issue #494](https://github.com/go-rod/rod/issues/494), proposed [PR #496](https://github.com/go-rod/rod/pull/496).

`Page.WaitLoad` waits for `document.readyState == "complete"` or the window load event through [the JavaScript waitLoad helper](../../lib/js/helper.js). It cannot return after parsing while images or other load-blocking resources remain pending. A raw DOMContentLoaded event waiter can miss the event when registered after it has already occurred.

The existing generic `Page.Wait` can already poll a ready-state predicate. This is a dedicated convenience-helper gap, not an inability to wait for parsing with current APIs.

Add `Page.WaitInteractive` and its Must wrapper, using the existing generated JavaScript-helper pattern. It should return immediately when the document is already interactive or complete, otherwise wait for parsing to finish. This remains a document-readiness helper; application-specific rendering and asynchronous API calls need their existing explicit waits.

Review the linked PR as implementation context, then adapt it to the current generator, context handling and test conventions. The helper is not available in the current API.

Acceptance criteria:

- With a local HTML fixture and an intentionally pending image, this helper completes after DOMContentLoaded while `WaitLoad` still waits.
- Calls made after DOMContentLoaded or after full load return immediately.
- Cancellation and navigation during the wait terminate or rebind according to the page wait contract, without listener leaks.
- Iframe behavior and Must error handling are covered.
- Update the JavaScript source and generated output together, with concise documentation distinguishing parsing from full load and DOM stability.
