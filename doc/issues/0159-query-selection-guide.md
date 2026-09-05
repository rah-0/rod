# Document query selection and waiting behavior together

Priority: P3, documentation. Source: [issue #159](https://github.com/go-rod/rod/issues/159). Recurring usage reports include [#1002](https://github.com/go-rod/rod/issues/1002), [#1032](https://github.com/go-rod/rod/issues/1032), and [#917](https://github.com/go-rod/rod/issues/917).

Reviewed: 2026-09-05.

## Current evidence

[query.go](../../query.go) implements materially different behavior for retrying single-element queries, immediate existence/list queries, DOM search, and races. The repository has API comments and examples but no guide comparing these choices or explaining their cancellation boundaries in one place. Reports repeatedly mistake an absent element for an immediate error from `Element`/`ElementX`.

Assessment: documentation gap verified statically; this task changes no query behavior.

## Scope

Add a concise guide linked from the README, with a comparison of `Element`, `ElementX`, `ElementR`, `ElementByJS`, `Elements`, `Has`, `Search`, and `Race`. Explain which operations retry, what absence returns, how to bound a query/race with a context, JavaScript function arguments, relative XPath under elements, and iframe/shadow-root scoping. Use the module import path and generic, self-contained examples.

## Acceptance

- Each documented behavior agrees with current source and existing tests.
- Examples demonstrate an optional element check and a bounded wait/race without unbounded hangs or live-site dependencies.
- Related error/Must semantics are explained briefly, and OOPIF limitations link to the appropriate supported behavior.
