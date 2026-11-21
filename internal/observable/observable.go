// Package observable provides Rod's internal ordered event fan-out.
package observable

import (
	"context"
	"errors"
	"sync"
)

// ErrOverflow means a subscriber exceeded its pending event budget.
var ErrOverflow = errors.New("rod: event subscription budget exceeded")

// Limits bounds pending events, including one blocked on delivery. Nonpositive
// fields select 4096 events and 16 MiB of event payload respectively.
type Limits struct {
	MaxEvents int
	MaxBytes  int
}

type limitsKey struct{}

// WithLimits configures subscriptions created with ctx.
func WithLimits(ctx context.Context, limits Limits) context.Context {
	return context.WithValue(ctx, limitsKey{}, limits)
}

func eventSize[T any](event T) int {
	if sized, ok := any(event).(interface{ EventSize() int }); ok {
		return max(1, sized.EventSize())
	}
	return 1
}

// Observable fans each published event out to every active subscriber.
type Observable[T any] struct {
	ctx context.Context

	mu          sync.Mutex
	subscribers map[<-chan T]func(T)
}

// New creates an Observable bound to ctx.
func New[T any](ctx context.Context) *Observable[T] {
	return &Observable[T]{
		ctx:         ctx,
		subscribers: make(map[<-chan T]func(T)),
	}
}

// Publish sends event to every current subscriber while preserving event order
// within each subscription.
func (o *Observable[T]) Publish(event T) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// Enqueueing never waits for a reader. Holding the lock also gives concurrent
	// publishers the same order in every subscription.
	for _, write := range o.subscribers {
		write(event)
	}
}

// Subscribe returns an ordered event stream that closes on cancellation or overflow.
func (o *Observable[T]) Subscribe(ctx context.Context) <-chan T {
	return o.SubscribeFilter(ctx, nil)
}

// SubscribeFilter subscribes only to matching events. A nil match accepts every
// event. The predicate runs during Publish and must not block or call Observable
// methods. Rejected events are never queued or delivered to the subscription.
func (o *Observable[T]) SubscribeFilter(ctx context.Context, match func(T) bool) <-chan T {
	_, events := o.SubscribeContext(ctx, match)
	return events
}

// SubscribeContext also returns the subscription context. Its cause reports
// ErrOverflow when the bounded queue cannot accept another matching event.
func (o *Observable[T]) SubscribeContext(ctx context.Context, match func(T) bool) (context.Context, <-chan T) {
	ctx, cancel := context.WithCancelCause(ctx)
	write, events := newPipe[T](ctx, cancel)
	if match != nil {
		enqueue := write
		write = func(event T) {
			if match(event) {
				enqueue(event)
			}
		}
	}

	o.mu.Lock()
	o.subscribers[events] = write
	o.mu.Unlock()

	stop := context.AfterFunc(o.ctx, func() { cancel(context.Cause(o.ctx)) })
	context.AfterFunc(ctx, func() {
		// Release the root callback when the subscription ends first. If it
		// has already started, its only action is the concurrency-safe cancel.
		stop()
		o.mu.Lock()
		delete(o.subscribers, events)
		o.mu.Unlock()
	})

	return ctx, events
}

// Len reports the current subscriber count.
func (o *Observable[T]) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.subscribers)
}

func newPipe[T any](ctx context.Context, cancel context.CancelCauseFunc) (func(T), <-chan T) {
	events := make(chan T)
	wake := make(chan struct{}, 1)
	limits, _ := ctx.Value(limitsKey{}).(Limits)
	if limits.MaxEvents <= 0 {
		limits.MaxEvents = 4096
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 16 << 20
	}

	var (
		mu    sync.Mutex
		queue []T
		head  int
		count int
		bytes int
	)

	write := func(event T) {
		if ctx.Err() != nil {
			return
		}
		mu.Lock()
		size := eventSize(event)
		if ctx.Err() != nil {
			mu.Unlock()
			return
		}
		if count >= limits.MaxEvents || size > limits.MaxBytes-bytes {
			cancel(ErrOverflow)
			queue = nil
			mu.Unlock()
			return
		}
		count++
		bytes += size
		// Compact only after consuming at least half the storage, so a steady
		// backlog does not require copying the whole queue for each event.
		if len(queue) == cap(queue) && head > 0 && head >= len(queue)/2 {
			remaining := copy(queue, queue[head:])
			clear(queue[remaining:])
			queue = queue[:remaining]
			head = 0
		}
		queue = append(queue, event)
		mu.Unlock()
		select {
		case wake <- struct{}{}:
		default:
		}
	}

	go func() {
		defer close(events)
		defer func() {
			mu.Lock()
			queue = nil
			mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}

			for {
				mu.Lock()
				if ctx.Err() != nil {
					mu.Unlock()
					return
				}
				if len(queue) == 0 {
					mu.Unlock()
					break
				}
				event := queue[head]
				var zero T
				queue[head] = zero
				head++
				if head == len(queue) {
					// Reuse ordinary bursts without retaining arbitrarily large
					// historical backlogs for the subscription's entire lifetime.
					if cap(queue) > 1024 {
						queue = nil
					} else {
						queue = queue[:0]
					}
					head = 0
				}
				mu.Unlock()

				select {
				case <-ctx.Done():
					return
				case events <- event:
				}
				mu.Lock()
				count--
				bytes -= eventSize(event)
				mu.Unlock()
			}
		}
	}()

	return write, events
}
