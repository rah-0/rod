# Avoid serializing load events from WaitLoad

Priority: P2.

Source: upstream [#1150](https://github.com/go-rod/rod/pull/1150).

## Current evidence

The `waitLoad` helper in [helper.js](../../lib/js/helper.js) and generated [helper.go](../../lib/js/helper.go) registers `resolve` directly as the load listener for both windows and elements. Event dispatch therefore resolves the promise with the event object. [Page.WaitLoad](../../page.go) and [Element.WaitLoad](../../element.go) request its value even though neither uses that result.

Evaluating the helper with a controlled element-like object that dispatches a circular load event reproduces CDP error `-32000: Object reference chain is too long`. Upstream discussion questioned whether `resolve` and `() => resolve()` differ; they do here because only the former receives the event argument.

## Change

Resolve successful load waits without a value in both event-driven branches. Preserve the immediate-completion branch and error rejection behavior. Edit the JavaScript source and regenerate with the fork's documented `go run ./lib/js/generate` command; do not copy the old minified generated output.

Replace the upstream test's fixed port, sleeps, and repeated navigations with controlled local fixtures or deterministic helper invocation. Synchronize listener registration and event dispatch so the test reliably exercises the deferred branch.

## Acceptance

- Deferred window and element load waits succeed when their load event contains a circular property.
- Successful load events are not returned for serialization.
- Already loaded resources return immediately, failed resources still report errors, and cancellation still terminates a pending wait.
- Generated helper output matches the source; relevant page and element load tests pass.
