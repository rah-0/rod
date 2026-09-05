# Accept an empty text regex in ElementR

Priority: P2. Source: [issue #958](https://github.com/go-rod/rod/issues/958), closed after the reporter switched to parsing HTML.

Reviewed: 2026-09-05.

## Current evidence

The reporter called `ElementR("a", "")` and received `TypeError: Cannot read properties of null (reading '3')`. The current [elementR JavaScript helper](../../lib/js/helper.js) still parses regex input with `regex.match(/(\/?)(.+)\1([a-z]*)/i)` and then reads `m[3]` unconditionally. An empty regex is valid JavaScript regex input, but this parser produces `null`.

Reproduced the current parsing expression in Node.js with an empty string; it throws the same TypeError. Element selection in a browser was not tested. Existing query tests contain no empty-regex case.

## Scope

Handle an empty regex with normal JavaScript empty-pattern semantics, or bypass the optional slash/flag parser when no match exists. Preserve existing plain-pattern and slash-delimited/flag syntax. Change the JavaScript source and regenerate [helper.go](../../lib/js/helper.go) together.

## Acceptance

- Page and element `ElementR` calls with an empty regex select the first matching CSS element, including one with empty text.
- `HasR` and race queries using the same helper behave consistently.
- An absent CSS element keeps the existing existence/retry semantics.
- Existing regex flags and invalid-regex error behavior remain covered; generation stays deterministic.
