package observable

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

type sizedTestEvent string

func (event sizedTestEvent) EventSize() int { return len(event) }

func TestObservableOverflow(t *testing.T) {
	for _, budget := range []string{"count", "bytes"} {
		t.Run(budget, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limits := Limits{MaxEvents: 2, MaxBytes: 100}
				if budget == "bytes" {
					limits = Limits{MaxEvents: 100, MaxBytes: 4}
				}
				o := New[sizedTestEvent](t.Context())
				ctx, events := o.SubscribeContext(WithLimits(t.Context(), limits), nil)
				otherCtx, other := o.SubscribeContext(t.Context(), nil)
				o.Publish("ab")
				synctest.Wait() // Includes the event blocked on delivery in the budget.
				o.Publish("cd")
				if ctx.Err() != nil {
					t.Fatal("exact budget overflowed")
				}
				o.Publish("e")
				synctest.Wait()
				if !errors.Is(context.Cause(ctx), ErrOverflow) {
					t.Fatalf("overflow cause = %v", context.Cause(ctx))
				}
				if _, open := <-events; open || o.Len() != 1 {
					t.Fatalf("overflow did not release subscription, Len=%d", o.Len())
				}
				for _, want := range []sizedTestEvent{"ab", "cd", "e"} {
					if got := <-other; got != want || otherCtx.Err() != nil {
						t.Fatalf("independent subscriber: %q, %v", got, otherCtx.Err())
					}
				}
			})
		})
	}
}

func TestObservableBudgetReusedAndFiltered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := New[sizedTestEvent](t.Context())
		ctx, events := o.SubscribeContext(WithLimits(t.Context(), Limits{MaxEvents: 1, MaxBytes: 2}), func(event sizedTestEvent) bool { return event != "rejected" })
		for range 10 {
			o.Publish("rejected")
			o.Publish("ab")
			if got := <-events; got != "ab" {
				t.Fatal(got)
			}
			synctest.Wait()
			if ctx.Err() != nil {
				t.Fatalf("consumption did not release budget: %v", context.Cause(ctx))
			}
		}
		o.Publish("abc")
		synctest.Wait()
		if !errors.Is(context.Cause(ctx), ErrOverflow) {
			t.Fatalf("oversized single event accepted: %v", context.Cause(ctx))
		}
	})
}
