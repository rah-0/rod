# Preserve caller cancellation while waiting for repaint

Priority: P1.

Sources: upstream [#1241](https://github.com/go-rod/rod/pull/1241), [#1225](https://github.com/go-rod/rod/pull/1225), and [#1219](https://github.com/go-rod/rod/pull/1219). These describe the same defect and are covered by this task.

## Current evidence

[Page.WaitRepaint](../../page.go) still evaluates `requestAnimationFrame` through `p.root.Eval`. [Page.Context](../../context.go) makes a shallow clone, so its root retains the original context. [Element.WaitStableRAF](../../element.go) supplies the element context to a page clone, but loses it again inside `WaitRepaint`. Element interactions that wait for stable animation frames can therefore outlive their deadline when a page stops repainting.

Calling `WaitRepaint` in Chrome with an already canceled page context returns `nil`, confirming that cancellation is ignored. The upstream reports additionally describe operations hanging for hours.

## Change

Evaluate on `p.root.Context(p.ctx)` to retain root-frame behavior and propagate cancellation. Keep this focused on context propagation; tracing from #1219 is optional. Adapt regression coverage to this fork's test helpers. If a test freezes a page, resolve the target element before freezing it so test setup cannot hang outside the intended deadline.

## Acceptance

- An already canceled page context returns `context.Canceled`.
- A repaint promise that never resolves returns `context.DeadlineExceeded` under a page or element timeout.
- `WaitStableRAF` terminates on cancellation without leaving its test goroutine blocked.
- Existing iframe interaction and normal repaint behavior remain covered; use the root execution context for iframe repaint waits.
