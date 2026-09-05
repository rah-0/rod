# Handle cross-process iframe sessions explicitly

Priority: P1 for safe failure; P2 for complete iframe support. Sources: [#1234](https://github.com/go-rod/rod/issues/1234), [#1076](https://github.com/go-rod/rod/issues/1076), [#548](https://github.com/go-rod/rod/issues/548), [#1081](https://github.com/go-rod/rod/issues/1081), [#949](https://github.com/go-rod/rod/issues/949), [#628](https://github.com/go-rod/rod/issues/628). Related duplicate reports: [#1012](https://github.com/go-rod/rod/issues/1012), [#805](https://github.com/go-rod/rod/issues/805), [#598](https://github.com/go-rod/rod/issues/598), [#541](https://github.com/go-rod/rod/issues/541).

Reviewed: 2026-09-05.

## Current evidence

[Element.Frame](../../element.go) clones the parent page and changes only the frame identity/runtime fields, retaining the parent's CDP session. [getJSCtxID](../../page_eval.go) then unconditionally dereferences `node.ContentDocument.BackendNodeID`. Cross-process frames may lack an in-process content document; the current code can panic instead of returning an ordinary error.

[Browser.Connect](../../browser.go) discovers targets but does not associate iframe sessions with frames. The [launcher's site-isolation flags](../../lib/launcher/launcher.go) are a workaround for locally launched Chrome and do not affect browsers attached through `ControlURL`. The fork's [README](../../README.md) advertises nested frames without explaining this boundary.

Assessment: source-verified session/lifecycle gap and nil dereference, supported by upstream reports; browser behavior was not independently reproduced. The reported parent-DOM fallback is not assumed to be the only possible failure.

## Scope

Represent and route out-of-process iframe work using its actual attached target/session, preserving same-process frames and navigation between the two forms. Handle missing, detached, and not-yet-ready content documents with bounded retry or an inspectable error. Never silently resolve an iframe against its parent's document. Document the external-browser boundary and any remaining unsupported cases; do not require weakening site isolation as the permanent solution.

## Acceptance

- Local fixtures on two distinct sites exercise same-process, cross-process, nested, detached, and navigating frames with site isolation enabled.
- Queries/evaluation reach the intended child document; unsupported/transitional states return an error or bounded retry instead of panicking.
- Context cancellation and session detach terminate work and release session state.
- Existing same-origin iframe/input/screenshot behavior remains covered.
