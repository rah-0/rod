# Avoid helper-cache writes after execution-context reset

Priority: P1. Sources: [issue #707](https://github.com/go-rod/rod/issues/707), [PR #1240](https://github.com/go-rod/rod/pull/1240). See the existing [pull assessment](../pulls/1240-helper-cache-reset.md).

Reviewed: 2026-09-05.

## Current evidence

[ensureJSHelper and setHelper](../../page_eval.go) release `helpersLock` while awaiting a CDP result. Concurrent evaluation can renew the execution context in `getJSCtxID`, clearing the whole helper cache or deleting one context entry. `setHelper` then writes through `p.helpers[jsCtxID][name]` without checking that the entry survived.

Reproduced using the current public `Page.Element` and `Page.Eval` APIs with a deterministic fake CDP client: pause helper creation, cause a concurrent evaluation to receive `cdp.ErrCtxNotFound` and renew its context, then resume helper creation. The evaluation succeeds, but the query panics with `assignment to entry in nil map`. This check did not use a browser. The upstream thread also contains a browser reproduction using a concurrent query and navigation.

## Scope

Make helper-cache storage tolerate invalidation under the existing shared lock. Do not recreate an obsolete context entry merely to suppress the panic. Preserve the existing retry/error behavior for functions created in a context that has since disappeared.

## Acceptance

- Whole-cache reset and individual-context deletion between helper lookup and storage do not panic or repopulate stale cache entries.
- Unchanged contexts continue to cache reusable helpers.
- A deterministic regression exercises public concurrent evaluation/query operations, passes with the race detector, and preserves normal navigation/helper behavior.
