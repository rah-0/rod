# Avoid helper-cache writes after an execution-context reset

Priority: P1.

Source: upstream [#1240](https://github.com/go-rod/rod/pull/1240), addressing [#707](https://github.com/go-rod/rod/issues/707). The PR was closed when its author deleted the head repository; it was not merged, and the defect remains in `setHelper`.

## Current evidence

In [page_eval.go](../../page_eval.go), `ensureJSHelper` releases the helper lock between obtaining a cache entry and creating its JavaScript function. `getJSCtxID` can clear the cache or remove a context entry before `setHelper` executes. The latter unconditionally assigns through `p.helpers[jsCtxID][name]`, which panics if that inner map has disappeared.

Resetting the whole cache or deleting the context entry before calling `setHelper` reproduces `assignment to entry in nil map`. Both reset paths need regression coverage.

## Change

While holding `helpersLock`, check that the execution context's cache entry still exists before storing the function. If reset removed it, leave the stale result uncached so the active context can create its own helper. Do not recreate the removed map simply to suppress the panic; that could retain a function belonging to an obsolete context.

Adapt the upstream deterministic test to this fork's module path and test conventions. This is independent of repaint-context propagation.

## Acceptance

- Whole-cache reset between lookup and store does not panic or repopulate a stale entry.
- Deleting one context entry has the same safe behavior.
- An unchanged context still caches and retrieves its function normally.
- Focused tests pass repeatedly and with the race detector; existing helper evaluation and navigation tests remain green.
