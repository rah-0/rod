package observable

import (
	"context"
	"testing"
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
