package observable

import (
	"context"
	"testing"
	"testing/synctest"
)

func TestObservableOrderAndCancellation(t *testing.T) {
	t.Parallel()

	root, stop := context.WithCancel(context.Background())
	streamCtx, cancel := context.WithCancel(context.Background())
	o := New[int](root)
	stream := o.Subscribe(streamCtx)

	for i := range 100 {
		o.Publish(i)
	}
	for i := range 100 {
		if got := <-stream; got != i {
			t.Fatalf("event %d = %d", i, got)
		}
	}

	cancel()
	if _, ok := <-stream; ok {
		t.Fatal("subscription remained open")
	}
	stop()
}

func TestObservableRootCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	o := New[string](ctx)
	stream := o.Subscribe(context.Background())
	cancel()
	if _, ok := <-stream; ok {
		t.Fatal("subscription remained open")
	}
}

func TestObservableCancellationCleanup(t *testing.T) {
	for _, source := range []string{"root", "subscription"} {
		for _, timing := range []string{"before subscribe", "after subscribe"} {
			t.Run(source+"/"+timing, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					root, stop := context.WithCancel(context.Background())
					defer stop()
					subscription, cancel := context.WithCancel(context.Background())
					defer cancel()
					cancelSource := cancel
					if source == "root" {
						cancelSource = stop
					}
					if timing == "before subscribe" {
						cancelSource()
					}

					o := New[int](root)
					stream := o.Subscribe(subscription)
					// Cancellation must also release an event pump blocked on delivery.
					o.Publish(1)
					synctest.Wait()
					cancelSource()
					synctest.Wait()

					if got := o.Len(); got != 0 {
						t.Fatalf("canceled subscription retained: Len() = %d", got)
					}
					if _, ok := <-stream; ok {
						t.Fatal("canceled subscription delivered an event")
					}
				})
			})
		}
	}
}

func TestObservableConcurrentCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root, stop := context.WithCancel(context.Background())
		defer stop()
		o := New[int](root)
		streams := make([]<-chan int, 32)
		start := make(chan struct{})
		for i := range streams {
			go func() {
				<-start
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				streams[i] = o.Subscribe(ctx)
				o.Publish(i)
			}()
		}
		go func() {
			<-start
			stop()
		}()
		close(start)
		synctest.Wait()

		if got := o.Len(); got != 0 {
			t.Fatalf("canceled subscriptions retained: Len() = %d", got)
		}
		for _, stream := range streams {
			if _, ok := <-stream; ok {
				t.Fatal("canceled subscription delivered an event")
			}
		}
	})
}
