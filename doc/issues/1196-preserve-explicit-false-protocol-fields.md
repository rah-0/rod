# Preserve explicit false for optional CDP boolean parameters

- Priority: P1
- Sources: [#1196](https://github.com/go-rod/rod/issues/1196), proposed patch [PR #1197](https://github.com/go-rod/rod/pull/1197).
- Reviewed: 2026-09-05.
- Validation: reproduced using the protocol types; `json.Marshal(proto.AccessibilityGetPartialAXTree{FetchRelatives: false})` returns `{}`.

In [accessibility.go](../../lib/proto/accessibility.go), `FetchRelatives` is `bool` with `omitempty`, even though the documented browser default is true. The typed request therefore cannot disable relatives. The generator in [generate/main.go](../../lib/proto/generate/main.go) adds presence pointers for optional numbers but has no corresponding boolean policy.

Audit optional boolean command parameters whose omitted value differs from explicit false. Give those parameters an explicit presence representation, update the generator and checked-in outputs together, and add wire tests. Review the broader heuristic from PR #1197 before adopting it: converting exported `bool` fields to `*bool` is a source compatibility change, and unknown defaults should not be guessed from comment wording. Use standard `encoding/json` as required by this fork.

Acceptance criteria:

- An omitted `fetchRelatives` remains absent; explicit false and true both serialize and reach CDP.
- A local accessibility fixture returns only the requested node when relatives are disabled.
- Other affected fields are identified from the pinned schema/documented defaults and covered by presence tests.
- Regeneration is deterministic; call sites and migration documentation reflect any exported field-type changes.
