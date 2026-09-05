# Respect the caller's context in WaitRepaint

- Priority: P1 — operations can hang beyond their advertised timeout.
- Source: [upstream issue #1179](https://github.com/go-rod/rod/issues/1179).
- Related proposals: [PR #1225](https://github.com/go-rod/rod/pull/1225), [PR #1241](https://github.com/go-rod/rod/pull/1241).
- Verified: 2026-09-05 with Chrome 152.0.7977.64.

## Current problem

[`Page.WaitRepaint`](../../page.go) evaluates `requestAnimationFrame` through `p.root.Eval`. [`Page.Context`](../../context.go) changes the clone's context but leaves its root pointing to the original page. The evaluation therefore ignores the caller's cancellation and deadline.

[`Element.WaitStableRAF`](../../element.go), and consequently scrolling and element interaction, depend on this wait. A pending animation-frame callback can leave an operation blocked indefinitely even when the caller supplied a timeout.

A local fixture replaced `window.requestAnimationFrame` with a function that never invokes its callback. `page.Timeout(100*time.Millisecond).WaitRepaint()` remained blocked after 400 ms and returned only after the parent browser context was canceled.

## Task

Keep evaluation on the root frame, while applying the invoking page's context to that evaluation. Preserve iframe behavior and existing public signatures. Apply any change to element waits only where they still lose the caller's context.

## Acceptance criteria

- A canceled page context makes `WaitRepaint` return a cancellation error.
- A pending animation-frame callback returns `context.DeadlineExceeded` when the derived page deadline expires, without canceling the browser.
- A bounded element operation waiting for frame stability also terminates on its own context.
- Normal root-frame and iframe waits still complete when a repaint occurs.
- Regression tests use local fixtures, a bounded outer guard, and clean up pending browser work.
