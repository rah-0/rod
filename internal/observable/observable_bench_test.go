package observable

import (
	"context"
	"fmt"
	"testing"
)

// Each iteration waits for every subscriber, so queued work cannot escape the
// measurement. The burst case also exercises subscribers that fall behind.
func BenchmarkObservableFanout(b *testing.B) {
	for _, subscribers := range []int{1, 8, 32} {
		for _, batch := range []int{1, 64} {
			b.Run(fmt.Sprintf("subscribers=%d/batch=%d", subscribers, batch), func(b *testing.B) {
				ctx, cancel := context.WithCancel(b.Context())
				defer cancel()
				o := New[int](ctx)
				streams := make([]<-chan int, subscribers)
				for i := range streams {
					streams[i] = o.Subscribe(ctx)
				}
				b.ReportAllocs()
				for b.Loop() {
					for event := range batch {
						o.Publish(event)
					}
					for _, stream := range streams {
						for event := range batch {
							if got := <-stream; got != event {
								b.Fatalf("event = %d, want %d", got, event)
							}
						}
					}
				}
				b.ReportMetric(float64(batch*subscribers), "deliveries/op")
			})
		}
	}
}

// A steady backlog catches queue implementations that copy all pending events
// whenever one event is consumed and another is published.
func BenchmarkObservableBacklog(b *testing.B) {
	for _, backlog := range []int{128, 4096} {
		b.Run(fmt.Sprintf("pending=%d", backlog), func(b *testing.B) {
			ctx, cancel := context.WithCancel(b.Context())
			defer cancel()
			o := New[int](ctx)
			stream := o.Subscribe(ctx)
			for event := range backlog {
				o.Publish(event)
			}
			read, next := 0, backlog
			b.ReportAllocs()
			for b.Loop() {
				if got := <-stream; got != read {
					b.Fatalf("event = %d, want %d", got, read)
				}
				o.Publish(next)
				read++
				next++
			}
			b.ReportMetric(float64(backlog), "pending")
		})
	}
}
