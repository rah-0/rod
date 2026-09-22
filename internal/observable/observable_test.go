package observable

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
)

func TestObservableConcurrentPublishOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	o := New[int](ctx)
	streams := make([]<-chan int, 8)
	for i := range streams {
		streams[i] = o.Subscribe(ctx)
	}
	const publishers, events = 8, 128
	start := make(chan struct{})
	var publishersDone sync.WaitGroup
	for publisher := range publishers {
		publishersDone.Go(func() {
			<-start
			for event := range events {
				o.Publish(publisher*events + event)
			}
		})
	}
	close(start)
	publishersDone.Wait()
	seen := make([]bool, publishers*events)
	for range seen {
		want := <-streams[0]
		if seen[want] {
			t.Fatalf("duplicate event %d", want)
		}
		seen[want] = true
		for _, stream := range streams[1:] {
			if got := <-stream; got != want {
				t.Fatalf("subscriber order differs: got %d, want %d", got, want)
			}
		}
	}
}

func TestObservableFilterAndUnreadSubscriber(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		o := New[int](ctx)
		unreadCtx, stopUnread := context.WithCancel(ctx)
		unread := o.Subscribe(unreadCtx)
		even := o.SubscribeFilter(ctx, func(event int) bool { return event%2 == 0 })
		rejected := o.SubscribeFilter(ctx, func(int) bool { return false })
		for event := range 512 {
			o.Publish(event)
		}
		for event := 0; event < 512; event += 2 {
			if got := <-even; got != event {
				t.Fatalf("filtered event = %d, want %d", got, event)
			}
		}
		stopUnread()
		synctest.Wait()
		if _, open := <-unread; open {
			t.Fatal("canceled unread subscription retained queued events")
		}
		o.Publish(512)
		if got := <-even; got != 512 {
			t.Fatalf("event after canceling another subscriber = %d", got)
		}
		cancel()
		synctest.Wait()
		if _, open := <-rejected; open {
			t.Fatal("filter delivered a rejected event")
		}
		if got := o.Len(); got != 0 {
			t.Fatalf("canceled subscribers retained: %d", got)
		}
	})
}

func TestObservableBacklogOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	o := New[int](ctx)
	stream := o.Subscribe(ctx)
	read := 0
	// Keep a backlog while alternating writes and reads, then drain and reuse it.
	for cycle := range 4 {
		for burst := range 8 {
			for event := range 64 {
				o.Publish(cycle*512 + burst*64 + event)
			}
			for range 32 {
				if got := <-stream; got != read {
					t.Fatalf("event = %d, want %d", got, read)
				}
				read++
			}
		}
		for read < (cycle+1)*512 {
			if got := <-stream; got != read {
				t.Fatalf("backlogged event = %d, want %d", got, read)
			}
			read++
		}
	}
}

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
