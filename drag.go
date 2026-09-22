package rod

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

var (
	// ErrDragInProgress means a page session already has a drag owned by Rod.
	ErrDragInProgress = errors.New("a drag is already in progress")
	// ErrDragEnded means MoveTo or Drop was called after the drag ended.
	ErrDragEnded = errors.New("drag has ended")
	// ErrInvalidDrag means the supplied drag data, position, or page is invalid.
	ErrInvalidDrag = errors.New("invalid drag parameters")
)

type dragStateKey proto.TargetSessionID

// Drag is a native HTML drag with explicitly supplied CDP data. A Drag must not
// be copied. Call Drop or Cancel to finish it; canceling the originating page
// context also cancels it. Use a page context deadline to bound its lifetime.
type Drag struct {
	mu          sync.Mutex
	page        *Page
	lifetime    context.Context
	data        *proto.InputDragData
	position    proto.Point
	stopContext context.CancelFunc
	stopCalls   context.CancelFunc
	stopAuto    func() bool
	done        bool
	err         error
}

// Drag enters a native HTML drag at position in the main frame's viewport, in
// CSS pixels. Use a top-level page and supply text, HTML, or link drag data.
// File drags are unsupported. Data is copied, and each event uses the page's
// currently held keyboard modifiers. This does not synthesize dragstart, press
// mouse buttons, move the mouse, or change drag interception settings.
// Only one Drag may be active per page session. Defer Cancel after success.
func (p *Page) Drag(position proto.Point, data *proto.InputDragData) (*Drag, error) {
	if p.IsIframe() {
		return nil, fmt.Errorf("%w: use a top-level page", ErrInvalidDrag)
	}
	if err := validateDragPosition(position); err != nil {
		return nil, err
	}
	if data == nil || len(data.Files) != 0 {
		return nil, fmt.Errorf("%w: supply drag data without files", ErrInvalidDrag)
	}
	owned := &proto.InputDragData{Items: make([]*proto.InputDragDataItem, len(data.Items)), DragOperationsMask: data.DragOperationsMask}
	for i, item := range data.Items {
		if item == nil {
			return nil, fmt.Errorf("%w: nil data item", ErrInvalidDrag)
		}
		copy := *item
		owned.Items[i] = &copy
	}
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	callCtx, stopCalls := context.WithCancel(ctx)
	drag := &Drag{page: p.Context(callCtx), lifetime: ctx, data: owned, position: position, stopContext: cancel, stopCalls: stopCalls}
	drag.mu.Lock()
	defer drag.mu.Unlock()
	if _, loaded := p.browser.states.LoadOrStore(dragStateKey(p.SessionID), drag); loaded {
		stopCalls()
		cancel()
		return nil, ErrDragInProgress
	}
	drag.stopAuto = context.AfterFunc(ctx, func() { _ = drag.Cancel() })
	if err := ctx.Err(); err != nil {
		return nil, drag.finish(err, false)
	}
	if err := drag.dispatch(drag.page, proto.InputDispatchDragEventTypeDragEnter, position); err != nil {
		return nil, drag.finish(err, true)
	}
	return drag, nil
}

// MoveTo sends drag-over at position in main-frame viewport CSS pixels.
// An invalid position leaves the drag active; a protocol error cancels it.
func (d *Drag) MoveTo(position proto.Point) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return errors.Join(ErrDragEnded, d.err)
	}
	if err := validateDragPosition(position); err != nil {
		return err
	}
	if err := d.dispatch(d.page, proto.InputDispatchDragEventTypeDragOver, position); err != nil {
		return d.finish(err, true)
	}
	d.position = position
	return nil
}

// Drop sends a final drag-over and drop at the last position, then releases the
// drag. If either command fails, it attempts cancellation within five seconds.
func (d *Drag) Drop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return errors.Join(ErrDragEnded, d.err)
	}
	if err := d.dispatch(d.page, proto.InputDispatchDragEventTypeDragOver, d.position); err != nil {
		return d.finish(err, true)
	}
	if err := d.dispatch(d.page, proto.InputDispatchDragEventTypeDrop, d.position); err != nil {
		return d.finish(err, true)
	}
	return d.finish(nil, false)
}

// Cancel ends the drag using an independent five-second cleanup context. It is
// safe to repeat, including after Drop, and returns any retained terminal error.
// Calling it after context cancellation waits for automatic cleanup to finish.
func (d *Drag) Cancel() error {
	// Interrupt a concurrent command before waiting for its lock.
	d.stopCalls()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return d.err
	}
	return d.finish(context.Cause(d.lifetime), true)
}

func (d *Drag) dispatch(page *Page, kind proto.InputDispatchDragEventType, position proto.Point) error {
	return (proto.InputDispatchDragEvent{
		Type: kind, X: position.X, Y: position.Y, Data: d.data,
		Modifiers: page.Keyboard.getModifiers(),
	}).Call(page)
}

// finish runs with mu held, so a new drag cannot overlap this one's cleanup.
func (d *Drag) finish(err error, cancelDrag bool) error {
	d.done = true
	d.stopAuto()
	if cancelDrag {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(d.page.ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, d.dispatch(d.page.Context(ctx), proto.InputDispatchDragEventTypeDragCancel, d.position))
	}
	d.err = err
	d.stopCalls()
	d.stopContext()
	d.page.browser.states.CompareAndDelete(dragStateKey(d.page.SessionID), d)
	return err
}

func validateDragPosition(position proto.Point) error {
	if math.IsNaN(position.X) || math.IsNaN(position.Y) || math.IsInf(position.X, 0) || math.IsInf(position.Y, 0) {
		return fmt.Errorf("%w: position must be finite", ErrInvalidDrag)
	}
	return nil
}
