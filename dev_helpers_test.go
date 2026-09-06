package rod_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/js"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

func TestMonitorCancellation(t *testing.T) {
	for _, timing := range []string{"before serve", "after serve"} {
		t.Run(timing, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if timing == "before serve" {
				cancel()
			}
			u := rod.New().Context(ctx).ServeMonitor("")
			transport := &http.Transport{DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			if timing == "after serve" {
				res, err := client.Get(u)
				if err != nil {
					t.Fatal(err)
				}
				_ = res.Body.Close()
				if res.StatusCode != http.StatusOK {
					t.Fatalf("monitor returned %s", res.Status)
				}
				cancel()
			}

			deadline := time.Now().Add(5 * time.Second)
			for {
				conn, err := net.DialTimeout("tcp", strings.TrimPrefix(u, "http://"), time.Second)
				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						t.Fatalf("monitor connection timed out: %v", err)
					}
					return
				}
				_ = conn.Close()
				if time.Now().After(deadline) {
					t.Fatal("canceled monitor is still accepting requests")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestMonitor(t *testing.T) {
	g := setup(t)

	b := rod.New().MustConnect()
	defer b.MustClose()
	p := b.MustPage(g.blank()).MustWaitLoad()

	b, cancel := b.WithCancel()
	defer cancel()
	host := b.Context(g.Context()).ServeMonitor("")

	const title = `<img src=x onerror="window.monitorXSS = true">`
	p.MustEval(`title => document.title = title`, title)

	page := g.page.MustNavigate(host)
	page.MustWait(`title => {
		const link = document.querySelector('#targets a')
		return link && link.textContent === title
	}`, title)
	g.False(page.MustHas("#targets img"))
	g.False(page.MustEval(`() => window.monitorXSS === true`).Bool())
	g.Has(page.MustElement("#targets a").MustParent().MustHTML(), string(p.TargetID))

	page.MustNavigate(host + "/page/" + string(p.TargetID))
	page.MustWait(`(id) => document.title.includes(id)`, p.TargetID)

	img := g.Req("", host+"/screenshot").Bytes()
	g.Gt(img.Len(), 10)

	res := g.Req("", host+"/api/page/test")
	g.Eq(400, res.StatusCode)
	g.Eq(-32602, jsonvalue.New(res.Body).Get("code").Int())
}

func TestMonitorErr(t *testing.T) {
	g := setup(t)

	l := launcher.New()
	defer func() {
		l.Kill()
		l.Cleanup()
	}()
	u := l.MustLaunch()

	g.Panic(func() {
		rod.New().Monitor("abc").ControlURL(u).MustConnect()
	})
}

func TestTrace(t *testing.T) {
	g := setup(t)

	g.Eq(rod.TraceTypeInput.String(), "[input]")

	var msg []any
	g.browser.Logger(utils.Log(func(list ...any) { msg = list }))
	g.browser.Trace(true).SlowMotion(time.Microsecond)
	defer func() {
		g.browser.Logger(rod.DefaultLogger)
		g.browser.Trace(defaults.Trace).SlowMotion(defaults.Slow)
	}()

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html")).MustWaitLoad()

	g.Eq(rod.TraceTypeWait, msg[0])
	g.Eq("load", msg[1])
	g.Eq(p, msg[2])

	el := p.MustElement("button")
	el.MustClick()

	g.Eq(rod.TraceTypeInput, msg[0])
	g.Eq("left click", msg[1])
	g.Eq(el, msg[2])

	g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
	_ = p.Mouse.MoveTo(proto.NewPoint(10, 10))
}

func TestTraceLogs(t *testing.T) {
	g := setup(t)

	g.browser.Logger(utils.LoggerQuiet)
	g.browser.Trace(true)
	defer func() {
		g.browser.Logger(rod.DefaultLogger)
		g.browser.Trace(defaults.Trace)
	}()

	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	el := p.MustElement("button")
	el.MustClick()

	g.mc.stubErr(1, proto.RuntimeCallFunctionOn{})
	p.Overlay(0, 0, 100, 30, "")
}

func TestExposeHelpers(t *testing.T) {
	g := setup(t)

	p := g.newPage(g.srcFile("fixtures/click.html"))
	p.ExposeHelpers(js.ElementR)

	g.Eq(p.MustElementByJS(`() => rod.elementR('button', 'click me')`).MustText(), "click me")
}
