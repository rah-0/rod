# Feature proposals

Five focused additions that reduce repeated browser automation and testing code.
These are proposals for future implementation, not available APIs. Selection is
based on source inspection of the current Rod API and representative browser
test workflows on 2026-09-05. The acceptance checks describe work to run during
implementation; they have not been executed for these proposals.

## Implementation order

| Order | Priority | Feature | Missing capability |
| --- | --- | --- | --- |
| 1 | P1 | [Page diagnostics](page-diagnostics.md) | Collect console output, unhandled JavaScript errors, and resource failures with reliable subscription and snapshot lifetimes. |
| 2 | P1 | [Owned configured launches](owned-configured-launches.md) | Retain process ownership when launching with explicit configuration, and provide bounded cleanup and useful startup errors. |
| 3 | P2 | [Strict JSON evaluation](strict-json-evaluation.md) | Serialize JavaScript results under an explicit JSON contract and decode them directly into Go destinations. |
| 4 | P2 | [Typing from strings](typing-from-strings.md) | Convert supported text to keyboard events without panics, control-key collisions, or partially typed invalid input. |
| 5 | P3 | [HTTP fixture pages](http-fixture-pages.md) | Reuse the setup and cleanup for HTML or handler-backed pages that need a real HTTP origin. |

P1 removes the most substantial lifecycle and event-handling code. P2 adds
small, explicit operations. P3 is optional test support and should remain
separate from the core browser API.

Each feature should compose with `Browser`, `Page`, `Element`, and the existing
context model. Keep the core dependency-free. Names and package placement are
design directions; the behavior and acceptance criteria define the scope.

## Existing capabilities to reuse

These workflows do not justify separate feature proposals:

| Workflow | Existing support |
| --- | --- |
| Wait for a condition, element, visibility, or repaint | `Page.Wait`, `Page.Element`, `Element.WaitVisible`, `Page.WaitRepaint`. |
| Click, double-click, and blur | `Element.Click` accepts the click count; `Element.Blur` removes focus. |
| Replace an editable value with Unicode text | `Element.SelectAllText` followed by `Element.Input`. |
| Change viewport or emulate a device | `Page.SetViewport`, `Page.Emulate`, and `Browser.DefaultDevice`; follow with an explicit wait when needed. |
| Capture viewport PNG or final HTML | `Page.Screenshot(false, ...)` and `Page.HTML`. |
| Decode an ordinary JSON evaluation result | `Page.Eval` followed by `result.Value.Unmarshal(&destination)`. Strict serialization is the additional behavior proposed above. |
| Find an installed browser and create a temporary profile | `launcher.LookPath` and `launcher.New`; automatic local launches already have process ownership in `Browser`. |
| Isolate browser storage | Separate browser profiles or `Browser.Incognito`, according to the required isolation. |
| Exercise storage, canvas, CSS, or application-specific DOM behavior | Existing JavaScript evaluation and page operations. |

The relevant implementations are in [page.go](../../page.go),
[query.go](../../query.go), [element.go](../../element.go),
[page_eval.go](../../page_eval.go), [jsonvalue](../../lib/jsonvalue/value.go),
[browser.go](../../browser.go), and [launcher](../../lib/launcher/launcher.go).

Keep action sequences as ordinary Go code. Test assertions, automatic failure
rules, missing-browser skips, fixed timeouts and viewport presets belong to
callers. A new runner, action interface, or assertion framework would duplicate
Rod's existing composition model.

Related correctness work remains in the [issue backlog](../issues/README.md)
and [pull-request assessments](../pulls/README.md). Individual proposals link
to relevant prerequisites without duplicating those tasks.
