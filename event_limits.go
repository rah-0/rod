package rod

import (
	"context"

	"github.com/rah-0/rod/internal/observable"
)

// ErrEventOverflow means a subscription exceeded its pending event count or
// payload byte budget. Waits fail instead of silently dropping matching events.
var ErrEventOverflow = observable.ErrOverflow

// EventBuffer returns a view whose event subscriptions use the given budgets.
// Nonpositive values select 4096 pending events and 16 MiB of protocol payload.
// Configure before Connect to also cover internal browser and page subscriptions.
func (b *Browser) EventBuffer(maxEvents, maxBytes int) *Browser {
	return b.Context(observable.WithLimits(b.ctx, observable.Limits{MaxEvents: maxEvents, MaxBytes: maxBytes}))
}

// EventBuffer returns a page view with bounded event subscriptions.
func (p *Page) EventBuffer(maxEvents, maxBytes int) *Page {
	return p.Context(observable.WithLimits(p.ctx, observable.Limits{MaxEvents: maxEvents, MaxBytes: maxBytes}))
}

// EventWithContext subscribes to browser events. context.Cause on the returned
// context distinguishes queue overflow from normal cancellation after it closes.
// Cancel the context passed to Browser.Context when abandoning the subscription.
func (b *Browser) EventWithContext() (context.Context, <-chan *Message) {
	return b.event.SubscribeContext(b.ctx, nil)
}

// EventWithContext subscribes to page events and exposes the termination cause.
func (p *Page) EventWithContext() (context.Context, <-chan *Message) {
	return p.event.SubscribeContext(p.ctx, nil)
}

// EventSize reports the retained protocol payload size used for event budgets.
// Decoded objects and Go allocation overhead are additional to this byte budget.
func (msg *Message) EventSize() int {
	return len(msg.data) + len(msg.Method) + len(msg.SessionID)
}
