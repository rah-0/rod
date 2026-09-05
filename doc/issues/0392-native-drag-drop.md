# Add a bounded helper for native HTML drag and drop

Priority: P3, feature design. Source: [issue #392](https://github.com/go-rod/rod/issues/392), related [#575](https://github.com/go-rod/rod/issues/575).

Reviewed: 2026-09-05.

## Current evidence

[Input protocol models](../../lib/proto/input.go) expose drag-enter, drag-over, drop, drag-cancel, drag data, and interception. The high-level [input API](../../input.go) has no drag lifecycle helper. [TestNativeDrag](../../input_test.go) is unconditionally skipped and attempts only mouse movement, unlike the enabled custom mouse-drag fixture.

Assessment: verified missing high-level functionality and skipped coverage. Existing raw CDP support remains usable; this is an optional ergonomic feature rather than a claim that all drag operations are broken.

## Scope

Define the smallest page-scoped helper that performs an explicit native drag sequence with supplied drag data or intercepted browser drag data. Preserve coordinates, modifier state, caller context, and a clear drop/cancel lifetime. Use the current protocol types; avoid a general gesture framework. Keep operating-system file drags outside the scope unless a separate reproducible need justifies them.

## Acceptance

- A local HTML5 `draggable`/drop-zone fixture observes the expected enter/over/drop events and data.
- Cancellation and explicit drag-cancel terminate the interaction and restore any owned interception setting.
- Browser movement-based drag behavior remains unchanged.
- Replace or complement the skipped native-drag case with deterministic coverage of the supported flow.
