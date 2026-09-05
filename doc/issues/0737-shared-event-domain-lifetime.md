# Keep shared event domains enabled until their last listener stops

Priority: P1 · Correctness · Confirmed with a synthetic CDP client on 2026-09-05.

Source: [upstream issue #737](https://github.com/go-rod/rod/issues/737).

`EachEvent` subscriptions for the same CDP domain interfere with one another. Listener A enables the domain, listener B observes it already enabled, and ending A disables it while B is still subscribed. B then silently stops receiving events. This also affects higher-level waits implemented through `EachEvent`.

The defect remains in [Browser.eachEvent](../../browser.go) and [Browser.EnableDomain](../../states.go): restoration captures the initial enabled boolean independently for each caller. There is no shared ownership accounting. The upstream maintainer acknowledged this limitation and suggested explicitly enabling domains as a workaround.

A browser-free reproduction registered two `Console.messageAdded` listeners, canceled only the first and waited for its cleanup. `Browser.LoadState("", &proto.ConsoleEnable{})` then returned false with the second listener still active. This confirms Rod's domain bookkeeping and emitted disable behavior; event delivery in a browser was not tested.

Implement synchronized lifetime accounting for automatic domain enablement, scoped to the underlying browser connection and CDP session/domain. Preserve a domain that was explicitly enabled before the subscriptions. Preserve the current event-handler API and independent cancellation behavior.

Acceptance criteria:

- Two overlapping listeners keep receiving events after either listener stops; the last automatic owner restores the original state.
- Cover both completion orders, cancellation, callback completion, same-domain multiple callback types and concurrent registrations.
- Sessions remain independent and no premature `*.disable` command is sent.
- Explicit pre-existing enablement remains enabled after all listeners finish.
- Race tests pass and shared listener lifecycle behavior is documented with the affected APIs.
