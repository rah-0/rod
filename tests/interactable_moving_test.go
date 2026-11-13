package rod_test

import (
	"context"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// The target starts where scrolling cannot bring it into view and comes into
// view in steps. After each step it rests for two animation frames or more, at
// a place that another element takes after a later step.
func TestWaitInteractableSteppingIntoView(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		// A timer moves the target up by 150 px every 35 ms.
		{"timer", `
			<div style="position: fixed; inset: 0"><button id=target style="position: absolute; left: 100px">target</button></div>
			<script>
				const target = document.getElementById('target')
				let y = innerHeight + 400
				target.style.top = y + 'px'
				window.start = () => {
					const timer = setInterval(() => {
						y = Math.max(y - 150, 100)
						target.style.top = y + 'px'
						if (y === 100) { clearInterval(timer); window.rests = true }
					}, 35)
				}
			</script>`},
		// A menu slides in from the left in 8 steps of 30 ms. The target is the
		// third of its buttons.
		{"transition", `
			<style>
				#menu { position: fixed; inset: 0 auto 0 0; width: 300px; display: flex; align-items: start;
					transform: translateX(-100%); transition: transform 240ms steps(8) }
				#menu.open { transform: none }
				#menu button { flex: none; width: 50px; height: 30px; margin: 0; padding: 0; border: 0 }
			</style>
			<div id=menu><button>a</button><button>b</button><button id=target>c</button><button>d</button></div>
			<script>
				const menu = document.getElementById('menu')
				menu.ontransitionend = () => { window.rests = true }
				window.start = () => menu.classList.add('open')
			</script>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := setup(t)
			p := g.page.MustNavigate(g.html(`<!doctype html><body style="margin: 0">` + test.body + `
				<script>
					addEventListener('click', e => {
						window.clicked = (e.target.id || e.target.localName) + (window.rests ? ' at rest' : ' on its way')
					}, true)
				</script>`)).MustWaitLoad()
			target := p.MustElement("#target").Timeout(5 * time.Second)
			defer target.CancelTimeout()

			p.MustEval(`() => window.start()`)
			err := target.Click(proto.InputMouseButtonLeft, 1)
			if err != nil {
				// Two tries in a row found the target at one place out of view.
				g.Is(err, &rod.InvisibleShapeError{})
				return
			}
			g.Eq(p.MustEval(`() => window.clicked`).Str(), "target at rest")
		})
	}
}

// The target stays where scrolling cannot bring it into view. When the sleeper
// or the context ends the wait, the error still says that the target is out of
// view.
func TestWaitInteractableOutOfViewEndsWait(t *testing.T) {
	g := setup(t)
	p := g.page.MustNavigate(g.html(`<!doctype html>
		<button id=target style="position: fixed; left: -300px">target</button>`)).MustWaitLoad()
	left := proto.InputMouseButtonLeft
	outOfView := func(err error) {
		t.Helper()
		if _, ok := err.(*rod.InvisibleShapeError); !ok {
			t.Fatalf("error = %T %v, want *rod.InvisibleShapeError", err, err)
		}
	}

	// These sleepers return an error instead of waiting.
	for _, sleeper := range []func() utils.Sleeper{
		rod.NotFoundSleeper,
		func() utils.Sleeper { return utils.CountSleeper(0) },
		// The error of a context that is not the one of the element.
		func() utils.Sleeper {
			return func(context.Context) error { return context.DeadlineExceeded }
		},
	} {
		el, err := p.Sleeper(sleeper).Element("#target")
		g.E(err)
		_, err = el.WaitInteractable()
		outOfView(err)
		outOfView(el.Hover())
		outOfView(el.Click(left, 1))
		outOfView(el.Tap())
	}

	// A race gives its branches such a sleeper. The error of the action ends the race.
	race := p.Timeout(3 * time.Second)
	defer race.CancelTimeout()
	_, err := race.Race().ElementFunc(func(p *rod.Page) (*rod.Element, error) {
		el, err := p.Element("#target")
		if err != nil {
			return nil, err
		}
		return el, el.Click(left, 1)
	}).Do()
	outOfView(err)

	// The context ends during the first sleep.
	el, cancel := p.MustElement("#target").WithCancel()
	defer cancel()
	_, err = el.Sleeper(func() utils.Sleeper {
		return func(ctx context.Context) error {
			cancel()
			return ctx.Err()
		}
	}).WaitInteractable()
	g.Is(err, &rod.InvisibleShapeError{})
	g.Is(err, context.Canceled)
}
