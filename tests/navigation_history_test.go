package rod_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

func TestWaitNavigationIgnoresExistingLifecycle(t *testing.T) {
	g := setup(t)
	page := g.page.MustNavigate(g.blank()).MustWaitLoad()
	ctx, cancel := context.WithTimeout(page.GetContext(), time.Second)
	defer cancel()
	wait := page.Context(ctx).WaitNavigation(proto.PageLifecycleEventNameLoad)
	if ctx.Err() != nil {
		t.Fatal("lifecycle setup exceeded the wait budget")
	}
	if err := wait(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("already loaded document satisfied a new navigation wait: %v", err)
	}
	ctx, stop := context.WithTimeout(page.GetContext(), 5*time.Second)
	defer stop()
	page = page.Context(ctx)
	wait = page.WaitNavigation(proto.PageLifecycleEventNameLoad)
	// Changing the query creates a new document in the same local fixture origin.
	g.E(page.Navigate(g.blank() + "?navigation=next"))
	g.E(wait())
	g.Eq(page.MustEval(`() => location.search`).Str(), "?navigation=next")
}
