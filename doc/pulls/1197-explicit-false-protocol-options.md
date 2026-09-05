# Preserve explicit false for protocol options with a true default

**Priority:** P1 — requested protocol behavior cannot be expressed.

**Sources:** [upstream PR #1197](https://github.com/go-rod/rod/pull/1197), [reproduction in issue #1196](https://github.com/go-rod/rod/issues/1196).

## Why this still applies

[AccessibilityGetPartialAXTree.FetchRelatives](../../lib/proto/accessibility.go) is a `bool` with `omitempty`, although the pinned protocol documentation says omission defaults to true. Standard `encoding/json` marshaling of `FetchRelatives: false` currently produces `{}`. The same problem exists for [PageCaptureScreenshot.FromSurface](../../lib/proto/page.go) and [RuntimeEvaluate.AllowUnsafeEvalBlockedByCSP](../../lib/proto/runtime.go). Marshaling any of these options as `false` omits the field.

The [presence tests](../../lib/proto/a_presence_test.go) cover JSON values and optional numbers, not these boolean request fields. PR #1197 was closed and its branch reset to upstream, leaving an empty current diff; the underlying defect remains independently verifiable.

## Task

Add a way to distinguish omitted, false, and true for the affected request options. Implement the representation in the [protocol generator](../../lib/proto/generate/main.go) and regenerate owned outputs from the pinned schema. Audit other optional request booleans whose omission has different semantics from false; avoid guessing defaults solely from prose matching.

Resolve source compatibility explicitly: converting exported `bool` fields to `*bool` breaks callers. Either provide a compatible explicit-presence mechanism or schedule and document that conversion for an intentional breaking release. Do not silently change existing zero-value request defaults or switch JSON packages.

## Acceptance criteria

- Wire tests distinguish absence, `false`, and `true` for each retained affected option.
- A browser regression verifies `fetchRelatives: false` excludes relatives.
- Generator fixture tests and repeat generation preserve the fix.
- Existing presence contracts remain intact; compatibility and caller changes are documented before adopting a breaking representation.
