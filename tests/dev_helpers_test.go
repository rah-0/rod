package rod_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/js"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

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
	g.Eq(host+"page/"+string(p.TargetID), page.MustElement("#targets a").MustProperty("href").String())

	page.MustNavigate(host + "page/" + string(p.TargetID))
	page.MustWait(`(id) => document.title.includes(id)`, p.TargetID)
	page.MustWait(`(url) => document.querySelector('.url').value === url &&
		document.querySelector('.screen').naturalWidth > 0`, p.MustInfo().URL)

	res := g.Req("", host+"screenshot/"+string(p.TargetID))
	g.Eq(http.StatusOK, res.StatusCode)
	g.True(bytes.HasPrefix(res.Bytes().Bytes(), []byte("\x89PNG")))

	res = g.Req("", host+"api/page/test")
	g.Eq(400, res.StatusCode)
	g.Eq(-32602, jsonvalue.New(res.Body).Get("code").Int())

	for _, path := range []string{"api/pages", "api/page/" + string(p.TargetID), "screenshot/" + string(p.TargetID)} {
		res = g.Req("", host+path, http.Header{"Host": {"rebind.example"}})
		g.Eq(http.StatusForbidden, res.StatusCode)
		res = g.Req("", strings.Replace(host, "/"+monitorToken(g, host)+"/", "/", 1)+path)
		g.Eq(http.StatusNotFound, res.StatusCode)
	}
}

// monitorToken returns the access token path segment of a monitor URL.
func monitorToken(g G, monitor string) string {
	g.Helper()
	_, path, _ := strings.Cut(strings.TrimPrefix(monitor, "http://"), "/")
	token := strings.TrimSuffix(path, "/")
	g.Gt(len(token), 20)
	return token
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

func TestTraceRedactsInput(t *testing.T) {
	g := setup(t)

	var lock sync.Mutex
	var logs []string
	g.browser.Logger(utils.Log(func(list ...any) {
		lock.Lock()
		defer lock.Unlock()
		logs = append(logs, fmt.Sprint(list...))
	}))
	g.browser.Trace(true)
	defer func() {
		g.browser.Logger(rod.DefaultLogger)
		g.browser.Trace(defaults.Trace)
	}()

	p := g.page.MustNavigate(g.html(`<html><body>
		<input id="text"><input id="date" type="date"><input id="color" type="color">
	</body></html>`)).MustWaitLoad()
	p.MustEval(recordTraceOverlays)

	const secret = `<img src=x onerror="window.tracePwned = true">hunter2`
	text := p.MustElement("#text").MustInput(secret)
	g.Eq(secret, text.MustProperty("value").String())
	text.MustType(input.KeyS, input.KeyE, input.Enter)
	g.Eq(secret+"se", text.MustProperty("value").String())
	p.MustElement("#date").MustInputTime(time.Date(1985, time.July, 14, 0, 0, 0, 0, time.Local))
	p.MustElement("#color").MustInputColor("#123456")

	leaks := []string{"hunter2", "<img", "KeyS", "KeyE", "1985", "#123456"}
	assertRedacted := func(where, text string) {
		g.Helper()
		for _, leaked := range leaks {
			if strings.Contains(text, leaked) {
				g.Errorf("%s contains %q:\n%s", where, leaked, text)
			}
		}
	}

	lock.Lock()
	joined := strings.Join(logs, "\n")
	lock.Unlock()
	assertRedacted("trace log", joined)
	g.Has(joined, fmt.Sprintf("insert text (%d characters redacted)", utf8.RuneCountInString(secret)))
	g.Has(joined, "press character key (redacted)")
	g.Has(joined, "press key: Enter")
	g.Has(joined, "input time (value redacted)")
	g.Has(joined, "input color (value redacted)")

	overlays := p.MustEval(`() => window.overlays`).Arr()
	g.Gt(len(overlays), 5)
	inserted := false
	for _, overlay := range overlays {
		overlayText := overlay.Get("text").Str()
		g.Eq(1, overlay.Get("elements").Int())
		assertRedacted("trace overlay", overlayText)
		inserted = inserted || strings.Contains(overlayText, "insert text (")
	}
	g.True(inserted)
	g.False(p.MustEval(`() => window.tracePwned === true`).Bool())
}

// recordTraceOverlays records the text and element count of each overlay added
// to the document root.
const recordTraceOverlays = `() => {
	window.overlays = []
	new MutationObserver(records => {
		for (const record of records) {
			for (const node of record.addedNodes) {
				if (node.tagName !== 'DIV') continue
				window.overlays.push({ text: node.textContent, elements: node.querySelectorAll('*').length })
			}
		}
	}).observe(document.documentElement, { childList: true })
}`

func TestTraceRedactsFrameInput(t *testing.T) {
	g := setup(t)

	g.browser.Logger(utils.LoggerQuiet)
	g.browser.Trace(true)
	defer func() {
		g.browser.Logger(rod.DefaultLogger)
		g.browser.Trace(defaults.Trace)
	}()

	// Different ports make the frame cross-origin to the top-level page.
	frameURL := g.html(`<html><body><input></body></html>`)
	p := g.page.MustNavigate(g.html(fmt.Sprintf(`<html><body><iframe src=%q></iframe></body></html>`, frameURL)))
	p.MustWaitLoad().MustEval(recordTraceOverlays)

	const secret = "frame secret"
	frame := p.MustElement("iframe").MustFrame()
	g.Eq(secret, frame.MustElement("input").MustInput(secret).MustProperty("value").String())

	inserted := false
	for _, overlay := range p.MustEval(`() => window.overlays`).Arr() {
		text := overlay.Get("text").Str()
		if strings.Contains(text, "secret") {
			g.Errorf("top-level overlay contains frame input: %s", text)
		}
		inserted = inserted || strings.Contains(text, "insert text (12 characters redacted)")
	}
	g.True(inserted)
}

func TestOverlayRendersText(t *testing.T) {
	g := setup(t)

	p := g.page.MustNavigate(g.html(`<html><body><p id="target">target</p></body></html>`)).MustWaitLoad()
	const msg = `<b id="overlay-markup">bold</b><img src=x onerror="window.overlayPwned = true">`
	overlays := `() => [...document.documentElement.children].filter(e => e.tagName === 'DIV').map(e => e.textContent)`

	remove := p.Overlay(10, 10, 100, 30, msg)
	g.Eq([]any{msg}, p.MustEval(overlays).Val())
	g.False(p.MustHas("#overlay-markup, img"))
	remove()
	g.Len(p.MustEval(overlays).Arr(), 0)

	remove = p.MustElement("#target").Overlay(msg)
	g.Eq([]any{msg}, p.MustEval(overlays).Val())
	g.False(p.MustHas("#overlay-markup, img"))
	remove()
	g.Len(p.MustEval(overlays).Arr(), 0)
	g.False(p.MustEval(`() => window.overlayPwned === true`).Bool())
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
	p.MustExposeHelpers(js.ElementR)

	g.Eq(p.MustElementByJS(`() => rod.elementR('button', 'click me')`).MustText(), "click me")

	// A page that makes the assignment throw causes an error, not a panic.
	p.MustEval(`() => { Object.defineProperty(window, "rod", { set() { throw new Error("hostile") } }) }`)
	var err error
	g.Nil(rod.Try(func() { err = p.ExposeHelpers(js.ElementR) }))
	var evalErr *rod.EvalError
	g.True(errors.As(err, &evalErr))
	g.Has(err.Error(), "hostile")
}

func TestMonitorCancellation(t *testing.T) {
	for _, timing := range []string{"before serve", "after serve"} {
		t.Run(timing, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if timing == "before serve" {
				cancel()
			}
			monitor := rod.New().Context(ctx).ServeMonitor("")
			u, err := url.Parse(monitor)
			if err != nil {
				t.Fatal(err)
			}
			transport := &http.Transport{DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			if timing == "after serve" {
				res, err := client.Get(monitor)
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
				conn, err := net.DialTimeout("tcp", u.Host, time.Second)
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

func TestMonitorAccessControl(t *testing.T) {
	browser := rod.New().Context(t.Context())
	monitor := browser.ServeMonitor("")
	u, err := url.Parse(monitor)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Trim(u.Path, "/")
	if len(token) < 26 || u.Path != "/"+token+"/" || strings.Contains(token, "/") {
		t.Fatalf("monitor URL has no access token path: %s", monitor)
	}
	if other := browser.ServeMonitor(""); strings.Contains(other, token) {
		t.Fatalf("monitors share an access token: %s and %s", monitor, other)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}

	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	for _, c := range []struct {
		name, host, path string
		status           int
	}{
		{"returned URL", u.Host, u.Path, http.StatusOK},
		{"localhost", "localhost:" + port, u.Path, http.StatusOK},
		{"localhost through a tunnel", "LOCALHOST:8080", u.Path, http.StatusOK},
		{"loopback range", "127.0.0.2:" + port, u.Path, http.StatusOK},
		{"IPv6 loopback", "[::1]:" + port, u.Path, http.StatusOK},
		{"page view", u.Host, u.Path + "page/target", http.StatusOK},
		{"rebinding name", "rebind.example:" + port, u.Path, http.StatusForbidden},
		{"rebinding name without port", "rebind.example", u.Path + "api/pages", http.StatusForbidden},
		{"localhost subdomain of another name", "localhost.rebind.example:" + port, u.Path, http.StatusForbidden},
		{"loopback-like name", "127.0.0.1.rebind.example:" + port, u.Path + "screenshot/target", http.StatusForbidden},
		{"other address", "192.0.2.1:" + port, u.Path + "api/page/target", http.StatusForbidden},
		{"missing token", u.Host, "/", http.StatusNotFound},
		{"missing token for API", u.Host, "/api/pages", http.StatusNotFound},
		{"missing token for screenshot", u.Host, "/screenshot/target", http.StatusNotFound},
		{"wrong token", u.Host, "/" + strings.ToLower(token) + "/", http.StatusNotFound},
		{"token prefix", u.Host, "/" + token[:len(token)-1] + "/api/pages", http.StatusNotFound},
		{"token without slash", u.Host, "/" + token, http.StatusMovedPermanently},
	} {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+u.Host+c.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = c.host
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != c.status {
				t.Fatalf("status %s, want %d: %s", res.Status, c.status, body)
			}
			if c.status == http.StatusMovedPermanently && res.Header.Get("Location") != u.Path {
				t.Fatalf("redirected to %q, want %q", res.Header.Get("Location"), u.Path)
			}
			if c.status != http.StatusOK && strings.Contains(string(body), "Rod Monitor") {
				t.Fatalf("rejected request returned the monitor: %s", body)
			}
		})
	}
}

// traceClient answers the calls made by input actions and trace overlays.
type traceClient struct {
	lock  sync.Mutex
	calls []traceCall
}

type traceCall struct {
	method string
	params string
}

func (c *traceClient) Event() <-chan *cdp.Event { return nil }

func (c *traceClient) Call(_ context.Context, _, method string, params any) ([]byte, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	c.lock.Lock()
	c.calls = append(c.calls, traceCall{method, string(data)})
	c.lock.Unlock()
	switch method {
	case "Runtime.evaluate":
		return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
	case "Runtime.callFunctionOn":
		return []byte(`{"result":{"type":"object","objectId":"helper"}}`), nil
	}
	return []byte(`{}`), nil
}

func TestTraceRedactsTypedInput(t *testing.T) {
	var lock sync.Mutex
	var logs []string
	client := &traceClient{}
	browser := rod.New().Context(t.Context()).Client(client).Trace(true).Logger(utils.Log(func(list ...any) {
		lock.Lock()
		defer lock.Unlock()
		logs = append(logs, fmt.Sprintln(list...))
	}))
	page := browser.PageFromSession("session")

	const secret = "correct horse battery"
	if err := page.InsertText(secret); err != nil {
		t.Fatal(err)
	}
	if err := page.Keyboard.Type(input.KeyZ, input.Digit9, input.Space, input.Tab, input.Enter); err != nil {
		t.Fatal(err)
	}
	if err := page.KeyActions().Press(input.ShiftLeft).Type(input.KeyQ).Do(); err != nil {
		t.Fatal(err)
	}

	lock.Lock()
	log := strings.Join(logs, "")
	lock.Unlock()
	client.lock.Lock()
	defer client.lock.Unlock()
	var overlays []string
	for _, call := range client.calls {
		switch call.method {
		case "Input.insertText", "Input.dispatchKeyEvent":
			continue
		case "Runtime.callFunctionOn":
			if strings.Contains(call.params, "[input]") {
				overlays = append(overlays, call.params)
			}
		}
		if strings.Contains(call.params, "horse") {
			t.Errorf("%s sent typed text to the page: %s", call.method, call.params)
		}
	}
	overlay := strings.Join(overlays, "\n")
	for _, leaked := range []string{"horse", "KeyZ", "Digit9", "Space", "KeyQ"} {
		if strings.Contains(log, leaked) || strings.Contains(overlay, leaked) {
			t.Errorf("trace output contains %q:\n%s\n%s", leaked, log, overlay)
		}
	}
	for _, want := range []string{
		"insert text (21 characters redacted)",
		"press character key (redacted)",
		"release character key (redacted)",
		"press key: Enter",
		"release key: Tab",
		"release key: ShiftLeft",
	} {
		if !strings.Contains(log, want) || !strings.Contains(overlay, want) {
			t.Errorf("trace output is missing %q:\n%s\n%s", want, log, overlay)
		}
	}
}
