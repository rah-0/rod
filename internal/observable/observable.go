// Package observable provides Rod's internal ordered event fan-out.
package observable

import (
	"context"
	"sync"
)

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
	writers := make([]func(T), 0, len(o.subscribers))
	for _, write := range o.subscribers {
		writers = append(writers, write)
	}
	o.mu.Unlock()

	for _, write := range writers {
		write(event)
	}
}

// Subscribe returns an ordered, lossless event stream that closes when either
// the subscription context or the Observable context ends.
func (o *Observable[T]) Subscribe(ctx context.Context) <-chan T {
	ctx, cancel := context.WithCancel(ctx)
	write, events := newPipe[T](ctx)

	o.mu.Lock()
	o.subscribers[events] = write
	o.mu.Unlock()

	stop := context.AfterFunc(o.ctx, cancel)
	context.AfterFunc(ctx, func() {
		// Release the root callback when the subscription ends first. If it
		// has already started, its only action is the concurrency-safe cancel.
		stop()
		o.mu.Lock()
		delete(o.subscribers, events)
		o.mu.Unlock()
	})

	return events
}

// Len reports the current subscriber count.
func (o *Observable[T]) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.subscribers)
}

func newPipe[T any](ctx context.Context) (func(T), <-chan T) {
	events := make(chan T)
	wake := make(chan struct{}, 1)

	var (
		mu    sync.Mutex
		queue []T
	)

	write := func(event T) {
		if ctx.Err() != nil {
			return
		}
		mu.Lock()
		queue = append(queue, event)
		mu.Unlock()
		select {
		case wake <- struct{}{}:
		default:
		}
	}

	go func() {
		defer close(events)
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}

			for {
				mu.Lock()
				if len(queue) == 0 {
					mu.Unlock()
					break
				}
				event := queue[0]
				var zero T
				queue[0] = zero
				queue = queue[1:]
				mu.Unlock()

				select {
				case <-ctx.Done():
					return
				case events <- event:
				}
			}
		}
	}()

	return write, events
}
