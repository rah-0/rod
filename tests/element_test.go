package rod_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/devices"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

func TestGetElementPage(t *testing.T) {
	g := setup(t)

	el := g.page.MustNavigate(g.blank()).MustElement("html")
	g.Eq(el.Page().SessionID, g.page.SessionID)
}

func TestClick(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.MustElement("button")
	el.MustClick()

	g.True(p.MustHas("[a=ok]"))

	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		el.MustClick()
	})
	g.Panic(func() {
		g.mc.stubErr(6, proto.RuntimeCallFunctionOn{})
		el.MustClick()
	})
}

func TestClickWrapped(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click-wrapped.html")).MustWaitLoad()
	el := p.MustElement("#target")

	shape := el.MustShape()
	g.Len(shape.Quads, 2)

	el.MustClick()
	g.True(p.MustHas("[a=ok]"))
}

func TestTap(t *testing.T) {
	g := setup(t)

	page := g.newPage()

	page.MustEmulate(devices.IPad).
		MustNavigate(g.srcFile("fixtures/touch.html")).
		MustWaitLoad()
	el := page.MustElement("button")

	el.MustTap()

	g.True(page.MustHas("[tapped=true]"))

	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		el.MustTap()
	})
	g.Panic(func() {
		g.mc.stubErr(1, proto.DOMScrollIntoViewIfNeeded{})
		el.MustTap()
	})
	g.Panic(func() {
		g.mc.stubErr(4, proto.RuntimeCallFunctionOn{})
		el.MustTap()
	})
	g.Panic(func() {
		g.mc.stubErr(7, proto.RuntimeCallFunctionOn{})
		el.MustTap()
	})
}

func TestInteractable(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.MustElement("button")
	g.True(el.MustInteractable())

	g.mc.stubErr(3, proto.RuntimeCallFunctionOn{})
	g.Err(el.Interactable())
}

func TestNotInteractable(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.MustElement("button")

	// cover the button with a green div
	p.MustWaitLoad().MustEval(`() => {
		let div = document.createElement('div')
		div.style = 'position: absolute; left: 0; top: 0; width: 500px; height: 500px;'
		document.body.append(div)
	}`)
	_, err := el.Interactable()
	g.Has(err.Error(), "element covered by: <div>")
	g.Is(err, &rod.NotInteractableError{})
	g.Is(err, &rod.CoveredError{})
	g.False(el.MustInteractable())
	ee, ok := errors.AsType[*rod.NotInteractableError](err)
	g.True(ok)
	g.Eq(ee.Error(), "element is not cursor interactable")

	p.MustElement("div").MustRemove()

	g.mc.stubErr(1, proto.DOMGetContentQuads{})
	_, err = el.Interactable()
	g.Err(err)

	g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
	g.Err(el.Interactable())

	g.mc.stubErr(1, proto.DOMResolveNode{})
	g.Err(el.Interactable())

	g.mc.stubErr(2, proto.RuntimeCallFunctionOn{})
	g.Err(el.Interactable())
}

type interactablePseudoCase struct {
	name   string
	style  string
	markup string
	hit    string
}

func TestInteractableOwnPseudoElement(t *testing.T) {
	for _, test := range []interactablePseudoCase{
		{"after", "#target::after { inset: -6px }", "<button id=target>ok</button>", "::after"},
		{"before", "#target::before { inset: 0 }", "<button id=target>ok</button>", "::before"},
		{"descendant", "#target > span::after { inset: -20px }", "<button id=target><span>ok</span></button>", "::after"},
		{"no pointer events", "#target::after { inset: -6px; pointer-events: none }", "<button id=target>ok</button>", "BUTTON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := setup(t)
			p := g.page.MustNavigate(g.html(`<!doctype html><style>
				body { margin: 40px } #target, span { position: relative }
				::before, ::after { content: ""; position: absolute }
				` + test.style + `</style>` + test.markup)).MustWaitLoad()
			el := p.MustElement("#target")
			hit := pseudoElementHit(t, el, test.hit)
			defer hit.MustRelease()
			point, err := el.Interactable()
			g.E(err)
			g.NotNil(point)
		})
	}
}

func TestInteractableCoveredByPseudoElement(t *testing.T) {
	for _, test := range []interactablePseudoCase{
		{"unrelated", "#cover { position: absolute; left: 40px; top: 40px } #cover::after { content: '' }", "<button id=target>ok</button><div id=cover></div>", "::after"},
		{"ancestor", "#cover { position: relative } #cover::after { content: '' }", "<div id=cover><button id=target>ok</button></div>", "::after"},
		{"element", "#cover { position: absolute; left: 40px; top: 40px }", "<button id=target>ok</button><div id=cover></div>", "DIV"},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := setup(t)
			p := g.page.MustNavigate(g.html(`<!doctype html><style>
				body { margin: 40px } #target, #cover { width: 100px; height: 50px }
				#cover::after { position: absolute; inset: 0 }
				` + test.style + `</style>` + test.markup)).MustWaitLoad()
			el := p.MustElement("#target")
			hit := pseudoElementHit(t, el, test.hit)
			defer hit.MustRelease()
			_, err := el.Interactable()
			covered, ok := errors.AsType[*rod.CoveredError](err)
			if !ok {
				t.Fatalf("Interactable error = %v, want CoveredError", err)
			}
			defer covered.MustRelease()
			g.Eq(covered.MustDescribe().NodeName, test.hit)
			if test.hit == "::after" {
				g.Has(err.Error(), "::after")
				if covered.Object.ClassName == "CSSPseudoElement" {
					g.Has(err.Error(), "div#cover::after")
				}
			} else {
				g.Has(err.Error(), "<div#cover>")
			}
			g.True(p.MustElement("#cover").MustContainsElement(covered.Element))
			g.True(p.MustElement("body").MustContainsElement(covered.Element))
			g.True(covered.MustContainsElement(covered.Element))
			g.False(el.MustContainsElement(covered.Element))
		})
	}
}

func TestInteractablePseudoElementActions(t *testing.T) {
	g := setup(t)
	p := g.newPage().MustEmulate(devices.IPad).MustNavigate(g.html(`<!doctype html>
		<style>
			body { margin: 40px }
			label { position: relative; display: inline-block; padding: 4px 12px }
			label::after { content: ""; position: absolute; inset: -3px -1px }
		</style>
		<input type=radio id=radio>
		<label for=radio onmouseenter="this.dataset.hovered = 'yes'"
			ontouchstart="this.dataset.tapped = 'yes'">1h</label>`)).MustWaitLoad()
	el := p.MustElement("label").Timeout(5 * time.Second)
	defer el.CancelTimeout()
	hit := pseudoElementHit(t, el, "::after")
	defer hit.MustRelease()
	point, err := el.WaitInteractable()
	g.E(err)
	g.NotNil(point)
	g.E(el.Hover())
	g.Eq(el.MustEval(`() => this.dataset.hovered`).Str(), "yes")
	start := time.Now()
	g.E(el.Click(proto.InputMouseButtonLeft, 1))
	g.Lt(time.Since(start), 4*time.Second)
	g.True(p.MustElement("#radio").MustProperty("checked").Bool())
	g.E(el.Tap())
	g.Eq(el.MustEval(`() => this.dataset.tapped`).Str(), "yes")
}

const interactablePseudoContextHTML = `<style>
	#target { position: relative; width: 100px; height: 50px }
	#target::after, #cover::after { content: ""; position: absolute; inset: 0 }
	#cover { display: none; position: absolute; top: 8px; left: 8px; width: 100px; height: 50px }
</style><button id=target>ok</button><div id=cover></div>`

func TestInteractablePseudoElementContexts(t *testing.T) {
	for _, name := range []string{"iframe", "shadow root"} {
		t.Run(name, func(t *testing.T) {
			g := setup(t)
			p := g.page.MustNavigate(g.html(`<!doctype html><div id=host></div><iframe></iframe>`)).Timeout(5 * time.Second)
			defer p.CancelTimeout()
			p.MustWaitLoad()
			var el, cover *rod.Element
			if name == "iframe" {
				p.MustElement("iframe").MustEval(`html => new Promise(resolve => {
					this.onload = () => resolve()
					this.srcdoc = html
				})`, interactablePseudoContextHTML)
				frame := p.MustElement("iframe").MustFrame()
				el = frame.MustElement("#target")
				cover = frame.MustElement("#cover")
			} else {
				p.MustElement("#host").MustEval(`html => this.attachShadow({mode: 'open'}).innerHTML = html`, interactablePseudoContextHTML)
				root := p.MustElement("#host").MustShadowRoot()
				el = root.MustElement("#target")
				cover = root.MustElement("#cover")
			}
			g.E(p.WaitRepaint())
			hit := pseudoElementHit(t, el, "::after")
			hit.MustRelease()
			point, err := el.Interactable()
			g.E(err)
			g.NotNil(point)
			cover.MustEval(`() => this.style.display = 'block'`)
			_, err = el.Interactable()
			covered, ok := errors.AsType[*rod.CoveredError](err)
			if !ok {
				t.Fatalf("Interactable error = %v, want CoveredError", err)
			}
			defer covered.MustRelease()
			g.Eq(covered.MustDescribe().NodeName, "::after")
			g.True(cover.MustContainsElement(covered.Element))
			g.False(el.MustContainsElement(covered.Element))
		})
	}
}

func TestInteractablePseudoElementMarker(t *testing.T) {
	g := setup(t)
	p := g.page.MustNavigate(g.html(`<!doctype html><style>
		body { margin: 40px } ol { list-style-position: inside; margin: 0; padding: 0 }
		#target { float: left }
	</style><ol><li id=target>.</li></ol>`)).MustWaitLoad()
	el := p.MustElement("#target")
	hit := pseudoElementHit(t, el, "::marker")
	defer hit.MustRelease()
	point, err := el.Interactable()
	g.E(err)
	g.NotNil(point)
}

func TestInteractablePseudoElementBackdrop(t *testing.T) {
	g := setup(t)
	p := g.page.MustNavigate(g.html(`<!doctype html><style>
		body { margin: 40px } #container { width: 100px; height: 100px }
		dialog { position: fixed; inset: 200px auto auto 200px; margin: 0 }
	</style><div id=container><button id=target>ok</button><dialog>modal</dialog></div>
	<script>document.querySelector('dialog').showModal()</script>`)).MustWaitLoad()
	el := p.MustElement("#target")
	hit := pseudoElementHit(t, el, "::backdrop")
	defer hit.MustRelease()
	_, err := el.Interactable()
	covered, ok := errors.AsType[*rod.CoveredError](err)
	if !ok {
		t.Fatalf("Interactable error = %v, want CoveredError", err)
	}
	defer covered.MustRelease()
	g.Eq(covered.MustDescribe().NodeName, "::backdrop")
	container := p.MustElement("#container")
	hit = pseudoElementHit(t, container, "::backdrop")
	defer hit.MustRelease()
	// The backdrop belongs to a descendant, just like a descendant's ::after.
	point, err := container.Interactable()
	g.E(err)
	g.NotNil(point)
}

// Verify the fixture exercises the expected hit, rather than an uncovered point.
func pseudoElementHit(t *testing.T, el *rod.Element, nodeName string) *rod.Element {
	t.Helper()
	point := el.MustShape().OnePointInside()
	if point == nil {
		t.Fatal("target has no point inside")
	}
	hit := el.Page().MustElementFromPoint(int(point.X), int(point.Y))
	if got := hit.MustDescribe().NodeName; got != nodeName {
		hit.MustRelease()
		t.Fatalf("hit node = %q, want %q", got, nodeName)
	}
	return hit
}

func TestInteractableWithNoShape(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/interactable.html"))

	el := p.MustElement("#no-shape")
	_, err := el.Interactable()
	g.Is(err, &rod.InvisibleShapeError{})
	g.Is(err, &rod.NotInteractableError{})
	g.Eq(err.Error(), "element has no visible shape or outside the viewport: <div#no-shape>")

	el = p.MustElement("#outside")
	_, err = el.Interactable()
	g.Is(err, &rod.InvisibleShapeError{})

	el = p.MustElement("#invisible")
	_, err = el.Interactable()
	g.Is(err, &rod.InvisibleShapeError{})
}

func TestNotInteractableWithNoPointerEvents(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/interactable.html"))
	_, err := p.MustElementR("#no-pointer-events", "click me").Interactable()
	g.Is(err, &rod.NoPointerEventsError{})
	g.Is(err, &rod.NotInteractableError{})
	g.Eq(err.Error(), "element's pointer-events is none: <span#no-pointer-events>")
}

func TestWaitInteractable(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.MustElement("button")

	start := time.Now()

	// cover the button with a green div for 1sec
	p.MustWaitLoad().MustEval(`() => {
		let div = document.createElement('div')
		div.style = 'position: absolute; left: 0; top: 0; width: 500px; height: 500px;'
		document.body.append(div)
		setTimeout(() => div.remove(), 1000)
	}`)

	el.MustWaitInteractable()

	g.Gt(time.Since(start), time.Second)

	g.mc.stubErr(1, proto.DOMScrollIntoViewIfNeeded{})
	g.Err(el.WaitInteractable())
}

func TestHover(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.MustElement("button")
	el.MustEval(`() => this.onmouseenter = () => this.dataset['a'] = 1`)
	el.MustHover()
	g.Eq("1", el.MustEval(`() => this.dataset['a']`).String())

	g.mc.stubErr(1, proto.DOMScrollIntoViewIfNeeded{})
	g.Err(el.Hover())

	g.mc.stubErr(1, proto.DOMGetContentQuads{})
	g.Err(el.Hover())

	g.mc.stubErr(3, proto.DOMGetContentQuads{})
	g.Err(el.Hover())

	g.mc.stubErr(1, proto.InputDispatchMouseEvent{})
	g.Err(el.Hover())
}

func TestElementMoveMouseOut(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	btn := p.MustElement("button")
	btn.MustEval(`() => this.onmouseout = () => this.setAttribute('name', 'mouse moved.')`)
	g.Eq("mouse moved.", *btn.MustHover().MustMoveMouseOut().MustAttribute("name"))

	g.mc.stubErr(1, proto.DOMGetContentQuads{})
	g.Err(btn.MoveMouseOut())
}

func TestElementContext(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.MustElement("button").Timeout(time.Hour).CancelTimeout()
	el, cancel := el.WithCancel()
	defer cancel()
	el.Sleeper(rod.DefaultSleeper).MustClick()
}

func TestElementCancelContext(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.Timeout(time.Second).MustElement("button")
	el = el.CancelTimeout()
	utils.Sleep(1.1)
	el.MustClick()
}

func TestIframes(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click-iframes.html"))

	frame01 := p.MustElement("iframe").MustFrame()

	frame02 := frame01.MustElement("iframe").MustFrame()
	el := frame02.MustElement("button")
	el.MustClick()

	g.Eq(frame01.MustEval(`() => testIsolation()`).Str(), "ok")
	g.True(frame02.MustHas("[a=ok]"))
}

func TestIframeCrossDomains(t *testing.T) {
	g := setup(t)

	r1 := g.Serve()
	r2 := g.Serve()

	// Same domain name with different ports won't trigger OOPIF (out-of-process iframes)
	// To check the page OOPIF status, you can use chrome://process-internals tab in the browser.
	host1 := net.JoinHostPort("localhost", r1.HostURL.Port())
	host2 := net.JoinHostPort("127.0.0.1", r2.HostURL.Port())

	u1 := fmt.Sprintf("http://%s/iframe", host1)
	u2 := fmt.Sprintf("http://%s/page", host2)

	r1.Route("/iframe", ".html", `<html>
		<div id="a">a</div>
	</html>`)

	r2.Route("/page", ".html", `<html>
		<iframe sandbox src="`+u1+`"></iframe>
	</html>`)

	l := launcher.New().HeadlessNew(true).NoSandbox(true)
	defer func() {
		l.Kill()
		l.Cleanup()
	}()
	u := l.MustLaunch()

	browser := rod.New().ControlURL(u).NoDefaultDevice().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage(u2)

	g.Eq(page.MustElement("iframe").MustFrame().MustElement("#a").MustText(), "a")
}

func TestContains(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	a := p.MustElement("button")

	b := p.MustElementFromNode(a.MustDescribe())
	g.True(a.MustContainsElement(b))

	pt := a.MustShape().OnePointInside()
	el := p.MustElementFromPoint(int(pt.X), int(pt.Y))
	g.True(a.MustContainsElement(el))

	g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
	g.Err(a.ContainsElement(el))
}

func TestShadowDOM(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/shadow-dom.html")).MustWaitLoad()
	el := p.MustElement("#container")
	g.Eq("inside", el.MustShadowRoot().MustElement("p").MustText())

	g.Panic(func() {
		g.mc.stubErr(1, proto.DOMDescribeNode{})
		el.MustShadowRoot()
	})
	g.Panic(func() {
		g.mc.stubErr(1, proto.DOMResolveNode{})
		el.MustShadowRoot()
	})

	elNoShadow := p.MustElement("script")
	_, err := elNoShadow.ShadowRoot()
	g.True((&rod.NoShadowRootError{}).Is(err))
	g.Has(err.Error(), "element has no shadow root:")
}

func TestInputTime(t *testing.T) {
	g := setup(t)

	now := time.Date(2006, 1, 2, 3, 4, 5, 0, time.Local)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))

	var el *rod.Element
	{
		el = p.MustElement("[type=date]")
		el.MustInputTime(now)

		g.Eq(el.MustText(), now.Format("2006-01-02"))
		g.True(p.MustHas("[event=input-date-change]"))
	}

	{
		el = p.MustElement("[type=datetime-local]")
		el.MustInputTime(now)

		g.Eq(el.MustText(), now.Format("2006-01-02T15:04"))
		g.True(p.MustHas("[event=input-datetime-local-change]"))
	}

	{
		el = p.MustElement("[type=time]")
		el.MustInputTime(now)

		g.Eq(el.MustText(), fmt.Sprintf("%02d:%02d", now.Hour(), now.Minute()))
		g.True(p.MustHas("[event=input-time-change]"))
	}

	{
		el = p.MustElement("[type=month]")
		el.MustInputTime(now)

		g.Eq(el.MustText(), now.Format("2006-01"))
		g.True(p.MustHas("[event=input-month-change]"))
	}

	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		el.MustInputTime(now)
	})
	g.Panic(func() {
		g.mc.stubErr(5, proto.RuntimeCallFunctionOn{})
		el.MustInputTime(now)
	})
	g.Panic(func() {
		g.mc.stubErr(6, proto.RuntimeCallFunctionOn{})
		el.MustInputTime(now)
	})
	g.Panic(func() {
		g.mc.stubErr(7, proto.RuntimeCallFunctionOn{})
		el.MustInputTime(now)
	})
}

func TestInputColor(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))

	var el *rod.Element
	{
		el = p.MustElement("[type=color]")
		el.MustInputColor("#ff6f00")

		g.Eq(el.MustText(), "#ff6f00")
		g.True(p.MustHas("[event=input-color-change]"))
	}

	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		el.MustInputColor("#ff6f00")
	})
	g.Panic(func() {
		g.mc.stubErr(5, proto.RuntimeCallFunctionOn{})
		el.MustInputColor("#ff6f00")
	})
	g.Panic(func() {
		g.mc.stubErr(6, proto.RuntimeCallFunctionOn{})
		el.MustInputColor("#ff6f00")
	})
}

func TestElementInputDate(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	p.MustElement("[type=date]").MustInput("12")
}

func TestCheckbox(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("[type=checkbox]")
	g.True(el.MustClick().MustProperty("checked").Bool())
}

func TestSelectText(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("textarea")
	el.MustInput("test")
	el.MustSelectAllText()
	el.MustInput("test")
	g.Eq("test", el.MustText())

	el.MustSelectText(`es`)
	el.MustInput("__")

	g.Eq("t__t", el.MustText())

	g.Panic(func() {
		g.mc.stubErr(1, proto.DOMScrollIntoViewIfNeeded{})
		el.MustSelectText("")
	})
	g.Panic(func() {
		g.mc.stubErr(1, proto.DOMScrollIntoViewIfNeeded{})
		el.MustSelectAllText()
	})

	g.Panic(func() {
		g.mc.stubErr(1, proto.DOMScrollIntoViewIfNeeded{})
		el.MustInput("")
	})
	g.Panic(func() {
		g.mc.stubErr(1, proto.InputInsertText{})
		el.MustInput("")
	})
}

func TestBlur(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("#blur").MustInput("test").MustBlur()

	g.Eq("ok", *el.MustAttribute("a"))
}

func TestSelectQuery(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("select")
	err := el.Select([]string{`[value="c"]`}, true, rod.SelectorTypeCSSSector)
	g.E(err)

	g.Eq(2, el.MustEval("() => this.selectedIndex").Int())
}

func TestSelectOptions(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("select")
	el.MustSelect("B", "C")
	g.Eq("B,C", el.MustText())
	g.Eq(1, el.MustProperty("selectedIndex").Int())

	// unselect with regex
	err := el.Select([]string{`^B$`}, false, rod.SelectorTypeRegex)
	g.E(err)
	g.Eq("C", el.MustText())

	// unselect with css selector
	err = el.Select([]string{`[value="c"]`}, false, rod.SelectorTypeCSSSector)
	g.E(err)
	g.Eq("", el.MustText())

	// option not found error
	g.Is(el.Select([]string{"not-exists"}, true, rod.SelectorTypeCSSSector), &rod.ElementNotFoundError{})

	{
		g.mc.stubErr(5, proto.RuntimeCallFunctionOn{})
		g.Err(el.Select([]string{"B"}, true, rod.SelectorTypeText))
	}
}

func TestMatches(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("textarea")
	g.True(el.MustMatches(`[cols="30"]`))

	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		el.MustMatches("")
	})
}

func TestAttribute(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("textarea")
	cols := el.MustAttribute("cols")
	rows := el.MustAttribute("rows")

	g.Eq("30", *cols)
	g.Eq("10", *rows)

	p = g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el = p.MustElement("button").MustClick()

	g.Eq("ok", *el.MustAttribute("a"))
	g.Nil(el.MustAttribute("b"))

	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		el.MustAttribute("")
	})
}

func TestProperty(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("textarea")
	cols := el.MustProperty("cols")
	rows := el.MustProperty("rows")

	g.Eq(float64(30), cols.Num())
	g.Eq(float64(10), rows.Num())

	p = g.page.MustNavigate(g.srcFile("fixtures/open-page.html"))
	el = p.MustElement("a")

	g.Eq("link", el.MustProperty("id").Str())
	g.Eq("_blank", el.MustProperty("target").Str())
	g.True(el.MustProperty("test").Nil())

	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		el.MustProperty("")
	})
}

func TestDisabled(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))

	g.False(p.MustElement("#EnabledButton").MustDisabled())
	g.True(p.MustElement("#DisabledButton").MustDisabled())

	g.Panic(func() {
		el := p.MustElement("#EnabledButton")
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		el.MustDisabled()
	})
}

func TestSetFiles(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement(`[type=file]`)
	el.MustSetFiles(
		repoPath("fixtures/click.html"),
		repoPath("fixtures/alert.html"),
	)

	list := el.MustEval("() => Array.from(this.files).map(f => f.name)").Arr()
	g.Len(list, 2)
	g.Eq("alert.html", list[1].String())
}

func TestEnter(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("[type=submit]")
	el.MustType(input.Enter)

	g.True(p.MustHas("[event=submit]"))
}

func TestWaitInvisible(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	h4 := p.MustElement("h4")
	btn := p.MustElement("button")

	g.True(h4.MustVisible())

	h4.MustWaitVisible()

	go func() {
		utils.Sleep(0.03)
		h4.MustEval(`() => this.remove()`)
		utils.Sleep(0.03)
		btn.MustEval(`() => this.style.visibility = 'hidden'`)
	}()

	h4.MustWaitInvisible()
	btn.MustWaitInvisible()

	g.False(p.MustHas("h4"))
}

func TestWaitEnabled(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	p.MustElement("button").MustWaitEnabled()
}

func TestWaitWritable(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	p.MustElement("input").MustWaitWritable()
}

func TestWaitStable(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/wait-stable.html"))
	el := p.MustElement("button")
	go func() {
		utils.Sleep(1)
		el.MustEval(`() => this.classList.remove("play")`)
	}()
	start := time.Now()
	el.MustWaitStable()
	g.Gt(time.Since(start), time.Second)

	ctx := g.Context()
	g.mc.stub(1, proto.DOMGetContentQuads{}, func(send StubSend) (jsonvalue.Value, error) {
		go func() {
			utils.Sleep(0.1)
			ctx.Cancel()
		}()
		return send()
	})
	g.Err(el.Context(ctx).WaitStable(time.Minute))

	g.Panic(func() {
		g.mc.stubErr(1, proto.DOMGetContentQuads{})
		el.MustWaitStable()
	})
	g.Panic(func() {
		g.mc.stubErr(2, proto.DOMGetContentQuads{})
		el.MustWaitStable()
	})
}

func TestWaitStableRAP(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/wait-stable.html"))
	el := p.MustElement("button")
	go func() {
		utils.Sleep(1)
		el.MustEval(`() => this.classList.remove("play")`)
	}()
	start := time.Now()
	g.E(el.WaitStableRAF())
	g.Gt(time.Since(start), time.Second)

	g.mc.stubErr(2, proto.RuntimeCallFunctionOn{})
	g.Err(el.WaitStableRAF())

	g.mc.stubErr(1, proto.DOMGetContentQuads{})
	g.Err(el.WaitStableRAF())
}

func TestCanvasToImage(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/canvas.html"))
	src, err := png.Decode(bytes.NewBuffer(p.MustElement("#canvas").MustCanvasToImage()))
	g.E(err)
	g.Eq(src.At(50, 50), color.NRGBA{0xFF, 0x00, 0x00, 0xFF})
}

func TestElementWaitLoad(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/resource.html"))
	p.MustElement("img").MustWaitLoad()
}

func TestResource(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/resource.html"))
	el := p.MustElement("img")
	g.Eq(len(el.MustResource()), 22661)

	g.mc.stub(1, proto.PageGetResourceContent{}, func(_ StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(proto.PageGetResourceContentResult{
			Content:       "ok",
			Base64Encoded: false,
		}), nil
	})
	g.Eq([]byte("ok"), el.MustResource())

	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		el.MustResource()
	})
	g.Panic(func() {
		g.mc.stubErr(1, proto.PageGetResourceContent{})
		el.MustResource()
	})
}

func TestBackgroundImage(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/resource.html")).MustWaitStable()
	el := p.MustElement("div")
	g.Eq(len(el.MustBackgroundImage()), 22661)

	{
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		g.Err(el.BackgroundImage())
	}
}

func TestElementScreenshot(t *testing.T) {
	g := setup(t)

	f := filepath.Join(t.ArtifactDir(), "element.png")
	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.MustElement("h4")

	data := el.MustScreenshot(f)
	img, err := png.Decode(bytes.NewBuffer(data))
	g.E(err)
	g.Eq(200, img.Bounds().Dx())
	g.Eq(30, img.Bounds().Dy())
	g.Nil(os.Stat(f))

	g.Panic(func() {
		g.mc.stubErr(1, proto.DOMScrollIntoViewIfNeeded{})
		el.MustScreenshot()
	})
	g.Panic(func() {
		g.mc.stubErr(1, proto.PageCaptureScreenshot{})
		el.MustScreenshot()
	})
	g.Panic(func() {
		g.mc.stubErr(3, proto.DOMGetContentQuads{})
		el.MustScreenshot()
	})
}

func TestUseReleasedElement(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	btn := p.MustElement("button")
	btn.MustRelease()
	g.Err(btn.Click("left", 1))

	btn = p.MustElement("button")
	g.E(proto.RuntimeReleaseObject{ObjectID: btn.Object.ObjectID}.Call(p))
	g.Is(btn.Click("left", 1), cdp.ErrObjNotFound)
}

func TestElementRemove(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	btn := p.MustElement("button")

	g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
	g.Err(btn.Remove())
}

func TestElementMultipleTimes(t *testing.T) {
	g := setup(t)

	// To see whether chrome will reuse the remote object ID or not.
	// Seems like it will not.

	page := g.page.MustNavigate(g.srcFile("fixtures/click.html"))

	btn01 := page.MustElement("button")
	btn02 := page.MustElement("button")

	g.Eq(btn01.MustText(), btn02.MustText())
	g.Neq(btn01.Object, btn02.Object)
}

func TestFnErr(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.MustElement("button")

	_, err := el.Eval("foo()")
	g.Err(err)
	g.Has(err.Error(), "ReferenceError: foo is not defined")
	e, ok := errors.AsType[*rod.EvalError](err)
	g.True(ok)
	g.Eq(proto.RuntimeRemoteObjectSubtypeError, e.Exception.Subtype)

	_, err = el.ElementByJS(rod.Eval("() => foo()"))
	g.Err(err)
	g.Has(err.Error(), "ReferenceError: foo is not defined")
	g.True(errors.Is(err, &rod.EvalError{}))
}

func TestElementEWithDepth(t *testing.T) {
	g := setup(t)

	checkStr := `green tea`
	p := g.page.MustNavigate(g.srcFile("fixtures/describe.html"))

	ulDOMNode, err := p.MustElement(`ul`).Describe(-1, true)
	g.Nil(errors.Unwrap(err))

	data, err := json.Marshal(ulDOMNode)
	g.Nil(errors.Unwrap(err))
	// The depth is -1, should contain checkStr
	g.Has(string(data), checkStr)
}

func TestElementOthers(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("form")
	el.MustFocus()
	el.MustScrollIntoView()
	g.Eq("submit", el.MustElement("[type=submit]").MustText())
	g.Eq("<input type=\"submit\" value=\"submit\">", el.MustElement("[type=submit]").MustHTML())
	el.MustWait(`() => true`)
	g.Eq("form", el.MustElementByJS(`() => this`).MustDescribe().LocalName)
	g.Len(el.MustElementsByJS(`() => []`), 0)
}

func TestElementEqual(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/describe.html"))
	el1 := p.MustElement("body > ul")
	el2 := p.MustElement("html > body > ul")
	g.True(el1.MustEqual(el2))

	el3 := p.MustElement("ul ul")
	g.False(el1.MustEqual(el3))
}

func TestElementWait(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/describe.html"))
	e1 := p.MustElement("body > ul > li")
	g.Eq(e1.MustText(), "coffee")

	params := []any{1, 3, 4}
	go func() {
		utils.Sleep(0.3)
		e1.MustEval(`(a, b, c) => this.innerText = 'x'.repeat(a + b + c)`, params...)
	}()

	e1.MustWait(`(a, b, c) => this.innerText.length === (a + b + c)`, params...)
	g.Eq(e1.MustText(), "xxxxxxxx")
}

func TestShapeInIframe(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click-iframe.html"))
	pt := p.MustElement("iframe").MustFrame().MustElement("button").MustShape().OnePointInside()

	g.InDelta(pt.X, 238, 1)
	g.InDelta(pt.Y, 287, 1)
}

func TestElementFromPointErr(t *testing.T) {
	g := setup(t)

	g.mc.stubErr(1, proto.DOMGetNodeForLocation{})
	g.Err(g.page.ElementFromPoint(10, 10))
}

func TestElementFromNodeErr(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.MustElementX("//button/text()")

	g.mc.stubErr(2, proto.RuntimeCallFunctionOn{})
	g.Err(p.ElementFromNode(el.MustDescribe()))
}

func TestElementErrors(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("form")

	ctx := g.Timeout(0)

	_, err := el.Context(ctx).Describe(-1, true)
	g.Err(err)

	_, err = el.Context(ctx).Frame()
	g.Err(err)

	err = el.Context(ctx).Focus()
	g.Err(err)

	_, err = el.Context(ctx).KeyActions()
	g.Err(err)

	err = el.Context(ctx).Input("a")
	g.Err(err)

	err = el.Context(ctx).Select([]string{"a"}, true, rod.SelectorTypeText)
	g.Err(err)

	err = el.Context(ctx).WaitStable(0)
	g.Err(err)

	_, err = el.Context(ctx).Resource()
	g.Err(err)

	err = el.Context(ctx).Input("a")
	g.Err(err)

	err = el.Context(ctx).Input("a")
	g.Err(err)

	_, err = el.Context(ctx).HTML()
	g.Err(err)

	_, err = el.Context(ctx).Visible()
	g.Err(err)

	_, err = el.Context(ctx).CanvasToImage("", 0)
	g.Err(err)

	err = el.Context(ctx).Release()
	g.Err(err)
}

func TestElementGetXPath(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.srcFile("fixtures/input.html"))
	el := p.MustElement("textarea")
	xpath := el.MustGetXPath(true)
	g.Eq(xpath, "/html/body/form/textarea")

	xpath = el.MustGetXPath(false)
	g.Eq(xpath, "/html/body/form/textarea")

	g.Panic(func() {
		g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
		el.MustGetXPath(true)
	})
}
