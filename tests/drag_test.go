package rod_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/proto"
)

type nativeDragEvent struct {
	Type    string
	X       float64
	Y       float64
	Ctrl    bool
	Trusted bool
}

func nativeDragData() *proto.InputDragData {
	return &proto.InputDragData{
		Items:              []*proto.InputDragDataItem{{MIMEType: "text/plain", Data: "native drag payload"}},
		DragOperationsMask: 1,
	}
}

func TestNativeDrag(t *testing.T) {
	g := setup(t)
	page := g.page.MustNavigate(g.srcFile("fixtures/native-drag.html")).MustWaitLoad()
	source := *page.MustElement("#source").MustShape().OnePointInside()
	target := *page.MustElement("#target").MustShape().OnePointInside()
	mousePosition := page.Mouse.Position()
	g.E(page.Keyboard.Press(input.ControlLeft))
	defer func() { g.E(page.Keyboard.Release(input.ControlLeft)) }()

	drag := page.MustDrag(source, nativeDragData())
	defer drag.MustCancel()
	drag.MustMoveTo(target).MustDrop()

	if got := page.MustEval(`() => window.dropped`).Str(); got != "native drag payload" {
		t.Fatalf("dropped data = %q", got)
	}
	var events []nativeDragEvent
	if err := page.MustEval(`() => window.dragEvents`).Unmarshal(&events); err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 || events[0].Type != "dragenter" || events[len(events)-1].Type != "drop" {
		t.Fatalf("unexpected native drag events: %+v", events)
	}
	over := false
	for _, event := range events {
		over = over || event.Type == "dragover"
		if event.X != target.X || event.Y != target.Y || !event.Ctrl || !event.Trusted {
			t.Errorf("native coordinates/modifiers/trust: %+v; target=%+v", event, target)
		}
	}
	if !over || page.Mouse.Position() != mousePosition {
		t.Fatalf("drag-over missing or mouse state changed: %+v", events)
	}
	if err := drag.MoveTo(source); !errors.Is(err, rod.ErrDragEnded) {
		t.Fatalf("move after drop: %v", err)
	}
}

func TestNativeDragCancel(t *testing.T) {
	for _, mode := range []string{"explicit", "context"} {
		t.Run(mode, func(t *testing.T) {
			g := setup(t)
			page := g.page.MustNavigate(g.srcFile("fixtures/native-drag.html")).MustWaitLoad()
			ctx, cancel := context.WithCancel(page.GetContext())
			defer cancel()
			source := *page.MustElement("#source").MustShape().OnePointInside()
			target := *page.MustElement("#target").MustShape().OnePointInside()
			drag := page.Context(ctx).MustDrag(source, nativeDragData()).MustMoveTo(target)
			if mode == "context" {
				cancel()
			}
			err := drag.Cancel()
			if mode == "context" && !errors.Is(err, context.Canceled) || mode == "explicit" && err != nil {
				t.Fatalf("cancel drag: %v", err)
			}
			if page.MustEval(`() => window.dropped`).Str() != "" {
				t.Fatal("canceled drag dispatched a drop")
			}
			page.MustDrag(source, nativeDragData()).MustMoveTo(target).MustDrop()
			if page.MustEval(`() => window.dropped`).Str() != "native drag payload" {
				t.Fatal("drag cancellation prevented a later drop")
			}
		})
	}
}
