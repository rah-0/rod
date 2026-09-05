# Collect page diagnostics with a defined lifecycle

Priority: P1. Status: proposed.

## Value and current support

A successful navigation or click does not establish that the page ran without
JavaScript errors or failed resources. Applications repeatedly build several
protocol listeners, request-ID maps, console formatters, and shutdown logic to
obtain that information.

[Page.EachEvent](../../page.go) and [rod.On](../../event.go) already expose typed
events. [Runtime events](../../lib/proto/runtime.go) report console calls and
exceptions; [Network events](../../lib/proto/network.go) report requests,
responses, and loading failures. `Page.WaitRequestIdle` tracks requests for
idleness, but does not produce a diagnostic report. The
[console example](../../examples_test.go) demonstrates listening, while
`Page.ObjectToJSON` reads a live remote object through another protocol call.

The missing feature is a reusable collector with structured records, readable
event-payload formatting, and an explicit start/snapshot/stop contract.

## Proposed behavior

Provide opt-in collection for a page, independent of a test framework. Starting
must establish readiness before the caller navigates or triggers an action,
and return errors if the necessary subscriptions or domains cannot be enabled.
Expose snapshots containing:

| Record | Contents |
| --- | --- |
| Console message | Console call type, rendered arguments, and available source URL, line, and column. |
| Page error | Unhandled exception or promise rejection text and available source location. |
| Resource failure | Request identity, URL, resource type, available HTTP status, transport or blocking reason, and cancellation flag. |

Use one-based source coordinates, with zero indicating unavailable coordinates.
Render console arguments from their supplied values, special primitive values,
descriptions, and previews. Include useful object, Map, and Set previews with
bounded depth and explicit truncation. Rendering must remain usable after
navigation and must not invoke getters or reevaluate logged objects. Document
that previews are abbreviated representations, not complete object snapshots
or a reproduction of Chrome DevTools console formatting.

Track exception IDs so a rejection that receives a matching revocation before
the snapshot boundary no longer appears as unhandled. Preserve console errors
as console messages; their level alone does not make them uncaught exceptions.

Record HTTP responses with status 400 or greater and transport loading failures.
Correlate failures with request URLs, update tracking across redirects, and
release completed request state. If an HTTP error response is followed by a
body-loading failure, retain both facts in one correlated record. Keep canceled
requests distinguishable so callers can decide whether they matter.

Snapshots must be safe during collection and must not expose mutable collector
storage. Bound retained records and rendered text, with visible truncation or
dropped-record counts. Define the policy for in-flight request tracking as well.

Stopping must be idempotent and bounded. Define a protocol-event boundary so
already queued diagnostics are processed before returning a final snapshot.
This boundary does not imply that future timers, promises, or network requests
have completed. If cancellation or target closure prevents a complete drain,
return the collected data together with an error indicating incomplete collection.
Remove owned subscriptions and any temporary bindings on every exit. Stop
coordination must not depend on page-owned console methods.

Initially cover events delivered to the page's CDP session and state the frame
scope precisely. Workers, popups, and cross-process iframe sessions require
separate target handling and are outside this proposal. Collection returns
data; callers choose logging, assertions, and failure policy.

## Implementation considerations

Reuse the existing event machinery, but account for its setup behavior:
[Browser.eachEvent](../../browser.go) enables domains before subscribing, and
[Browser.EnableDomain](../../states.go) does not return enable failures. A
collector must expose setup errors and establish its documented readiness
boundary without silently losing events emitted during setup.

Coordinate domain ownership with
[shared event-domain lifetime](../issues/0737-shared-event-domain-lifetime.md).
Stopping a collector must preserve Runtime or Network domains used by other
listeners. Keep formatting and event-drain coordination internal unless another
concrete use requires public APIs.

This is passive observation. The existing
[browser response-capture proposal](../issues/0607-browser-response-capture.md)
concerns response bodies and interception, which are separate capabilities.

## Acceptance

- Capture console output and unhandled errors from scripts executed during
  initial navigation. Test setup failure without leaking subscriptions.
- Cover primitive and special values, nested and cyclic previews, Maps, Sets,
  missing locations, and truncated payloads without extra object evaluation.
- Exclude caught exceptions and handled rejections; remove a previously recorded
  exception when its matching revocation arrives before the boundary.
- Cover HTTP 404/500, transport failure, blocking, cancellation, redirects,
  unknown request IDs, and an HTTP failure followed by a body failure. Release
  completed and failed request tracking.
- Verify snapshot independence and retention limits while events arrive.
- Verify repeated stop, immediate stop after an action, context expiry,
  navigation, and target closure. Report incomplete draining and preserve data.
- Verify cleanup with concurrent Runtime/Network subscribers, and ensure any
  temporary browser bindings are removed.
- Use local HTTP fixtures for browser coverage and deterministic event sequences
  for correlation, revocation, ordering, and concurrency checks.
