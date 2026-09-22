# Native HTML drag and drop

Run from the repository root:

```sh
go run ./examples/drag
```

Expected output:

```text
Dropped: hello from Rod
```

The [example](main.go) launches a headless browser and serves the embedded
[page](page.html) through a local HTTP fixture. It locates the source and target,
supplies a `text/plain` payload to `Page.Drag`, moves to the target with `MoveTo`,
and calls `Drop`. It verifies the payload received through `event.dataTransfer`
before printing the result. The operation has a context deadline and defers
`Cancel` for cleanup.

Use a top-level page and main-frame viewport coordinates in CSS pixels.
`Page.Drag` sends drag-enter, `MoveTo` sends drag-over, and `Drop` sends a final
drag-over followed by drop at the last position. `DragOperationsMask` selects the
allowed operations: Copy=1, Link=2, and Move=16. Each command carries the keyboard
modifiers currently held through Rod's `Keyboard`. The helper neither presses
nor releases keys or mouse buttons and does not change Rod's tracked mouse
position.

Only one helper-owned drag can be active per page session, including its context
clones. Finish it with `Drop` or `Cancel`. Canceling the page context also cancels
the drag; cleanup uses an independent five-second budget. `Cancel` is idempotent
and returns any retained terminal error. It can be called after context
cancellation to wait for cleanup. Further moves or drops return `ErrDragEnded`.
Errors during enter, over, or drop also trigger cancellation. Check returned
errors to detect a failed cleanup command.

The helper copies the supplied data and rejects iframe views, file payloads,
nil data or data items, and non-finite coordinates with `ErrInvalidDrag`.
It supports text, links, and HTML. It does not synthesize a source `dragstart`
event or extract data from a draggable DOM element. Supply the payload explicitly, or
use data obtained separately through the existing `Input.dragIntercepted`
protocol event. The helper does not change `Input.setInterceptDrags`; callers
that enable interception own restoring that setting.

Mouse movement-based dragging remains available through `Mouse` for applications
that implement their own pointer gestures. Operating-system file dragging is
outside this helper's scope; use `Element.SetFilesFromMemory` for file inputs.
The corresponding Must helpers panic on errors.

The [example test](main_test.go) checks the runnable result. The
[core browser tests](../../tests/drag_test.go) also cover native events, coordinates,
modifier state, cancellation, and a subsequent successful drop.
