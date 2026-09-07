# Cancel WaitOpen when the opener page session ends

Priority: P2, page lifecycle correctness.

Confirmed on 2026-09-07 against v0.120.0 (`5fab1e1`), using a mock CDP client with Go 1.27.1 on Linux/amd64 and the race detector. No browser is required for the reproduction below.

## Current evidence

If an opener page is destroyed before opening a popup, an outstanding `Page.WaitOpen()` wait remains blocked while the caller and browser contexts remain active. Without a caller-supplied deadline or cancellation, the wait can remain pending indefinitely.

[`Page.WaitOpen`](../../page.go) subscribes through `p.browser.Context(p.ctx).EachEvent`. Page views created by [`Browser.pageView`](../../browser.go) use the caller's operation context, while target destruction ends the separate page session context. The browser-wide subscription does not incorporate the opener's session context. Other page wait helpers, including `Page.EachEvent`, combine these lifetimes using `contextWithSession`.

## Reproduction

1. Connect a browser to a mock CDP client and obtain a page with `PageFromTarget`.
2. Subscribe to `Page.Event` and start the function returned by `Page.WaitOpen`.
3. Deliver `Target.targetDestroyed` for the opener, keeping the caller context and CDP event channel open.
4. Observe that `Page.Event` closes, but the popup wait does not return within the observation interval.
5. Cancel the caller context. The popup wait then returns `context canceled`.

<details>
<summary>Standalone reproduction</summary>

Save this program outside the repository as `waitopen-repro.go`. From the repository root, run `go run -race /path/to/waitopen-repro.go`.

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
)

type client struct{ events chan *cdp.Event }

func (c *client) Event() <-chan *cdp.Event { return c.events }

func (c *client) Call(ctx context.Context, _, method string, _ any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if method == "Target.attachToTarget" {
		return []byte(`{"sessionId":"opener-session"}`), nil
	}
	return []byte(`{}`), nil
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client{events: make(chan *cdp.Event, 1)}
	browser := rod.New().NoDefaultDevice().Context(ctx).Client(c)
	if err := browser.Connect(); err != nil {
		panic(err)
	}
	page, err := browser.PageFromTarget("opener")
	if err != nil {
		panic(err)
	}
	pageEvents := page.Event()
	wait := page.WaitOpen()
	done := make(chan error, 1)
	go func() {
		_, err := wait()
		done <- err
	}()
	c.events <- &cdp.Event{
		Method: "Target.targetDestroyed",
		Params: json.RawMessage(`{"targetId":"opener"}`),
	}
	select {
	case _, open := <-pageEvents:
		if open {
			panic("unexpected page event")
		}
	case <-time.After(time.Second):
		panic("page event stream did not close")
	}
	fmt.Println("Page.Event closed; caller context remains active:", ctx.Err() == nil)
	select {
	case err := <-done:
		fmt.Println("WaitOpen returned after session termination:", err)
		return
	case <-time.After(250 * time.Millisecond):
		fmt.Println("WaitOpen remains pending after session termination")
	}
	cancel()
	select {
	case err := <-done:
		fmt.Println("WaitOpen returned after caller cancellation:", err)
	case <-time.After(time.Second):
		panic("WaitOpen did not return after caller cancellation")
	}
}
```

Observed output on v0.120.0:

```text
Page.Event closed; caller context remains active: true
WaitOpen remains pending after session termination
WaitOpen returned after caller cancellation: context canceled
```

</details>

## Scope and acceptance

Tie the pending popup wait to the opener's session lifetime as well as the caller and browser lifetimes. Session termination before a matching popup event should return a cancellation or session-termination error and release the subscription.

- Add regression coverage for opener destruction and session detachment while the browser remains connected.
- Preserve caller cancellation, deadline, and browser-disconnection errors.
- A matching popup still returns successfully; unrelated target-created events do not complete the wait.
- Releasing the wait's temporary context must not cancel the returned popup's operations. The popup retains the caller's intended operation lifetime.
- Verify subscription cleanup and race-detector results using deterministic event synchronization in the regression tests.
