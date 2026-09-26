package main_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/proto"
)

// BenchmarkElementResolution reports the CDP calls and latency of element
// lookups and pointer checks, locally and with a simulated round-trip time.
func BenchmarkElementResolution(b *testing.B) {
	browser, client, _ := benchmarkCountingBrowser(b)
	// Touching every same-process frame caches all their contexts on the page.
	const frames = 10
	framed := browser.MustPage("")
	framed.MustSetDocumentContent(`<button>ok</button>` +
		strings.Repeat(`<iframe srcdoc="<p>frame</p>"></iframe>`, frames)).MustWaitLoad()
	frameViews := []*rod.Page{}
	for _, iframe := range framed.MustElements("iframe") {
		frame := iframe.MustFrame()
		frame.MustElement("p").MustRelease()
		frameViews = append(frameViews, frame)
	}

	// Clicks wait for animation frames, which only the foreground page renders.
	plain := browser.MustPage("").MustActivate()
	plain.MustSetDocumentContent(`<button style="width:80px;height:30px">ok</button><div><span>child</span></div>`).MustWaitLoad()
	button := plain.MustElement("button")
	span := plain.MustElement("span")
	rotation := 0

	scenarios := []struct {
		name string
		op   func(*testing.B) []*rod.Element
	}{
		{"Element", func(b *testing.B) []*rod.Element {
			return []*rod.Element{plain.Context(b.Context()).MustElement("button")}
		}},
		{"Parent", func(b *testing.B) []*rod.Element {
			return []*rod.Element{span.Context(b.Context()).MustParent()}
		}},
		{"Interactable", func(b *testing.B) []*rod.Element {
			if _, err := button.Context(b.Context()).Interactable(); err != nil {
				b.Fatal(err)
			}
			return nil
		}},
		{"Click", func(b *testing.B) []*rod.Element {
			if err := button.Context(b.Context()).Click(proto.InputMouseButtonLeft, 1); err != nil {
				b.Fatal(err)
			}
			return nil
		}},
		{"ElementAfterFrames", func(b *testing.B) []*rod.Element {
			return []*rod.Element{framed.Context(b.Context()).MustElement("button")}
		}},
		{"FrameElement", func(b *testing.B) []*rod.Element {
			return []*rod.Element{frameViews[frames-1].Context(b.Context()).MustElement("p")}
		}},
		{"Elements", func(b *testing.B) []*rod.Element {
			return plain.Context(b.Context()).MustElements("button, span")
		}},
		// A new view resolves the frame's context among all cached contexts.
		{"NewFrameView", func(b *testing.B) []*rod.Element {
			iframe := framed.Context(b.Context()).MustElement("iframe:last-of-type")
			return []*rod.Element{iframe, iframe.MustFrame().MustElement("p")}
		}},
		// Views of different frames in turn.
		{"NewFrameViewRotating", func(b *testing.B) []*rod.Element {
			rotation++
			iframe := framed.Context(b.Context()).MustElement(fmt.Sprintf("iframe:nth-of-type(%d)", rotation%frames+1))
			return []*rod.Element{iframe, iframe.MustFrame().MustElement("p")}
		}},
	}
	for _, rtt := range []time.Duration{0, 2 * time.Millisecond} {
		for _, scenario := range scenarios {
			b.Run(fmt.Sprintf("%s/rtt=%s", scenario.name, rtt), func(b *testing.B) {
				client.rtt.Store(int64(rtt))
				defer client.rtt.Store(0)
				var calls, ops int64
				for b.Loop() {
					start := client.calls.Load()
					elements := scenario.op(b)
					calls += client.calls.Load() - start
					ops++
					b.StopTimer()
					for _, el := range elements {
						if err := el.Release(); err != nil {
							b.Fatal(err)
						}
					}
					b.StartTimer()
				}
				b.ReportMetric(float64(calls)/float64(ops), "calls/op")
			})
		}
	}
}
