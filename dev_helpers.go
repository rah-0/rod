// This file defines the helpers to develop automation.
// Such as when running automation we can use trace to visually
// see where the mouse going to click.

package rod

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rah-0/rod/lib/assets"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/js"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// TraceType for logger.
type TraceType string

// String interface.
func (t TraceType) String() string {
	return fmt.Sprintf("[%s]", string(t))
}

const (
	// TraceTypeWaitRequestsIdle type.
	TraceTypeWaitRequestsIdle TraceType = "wait requests idle"

	// TraceTypeWaitRequests type.
	TraceTypeWaitRequests TraceType = "wait requests"

	// TraceTypeQuery type.
	TraceTypeQuery TraceType = "query"

	// TraceTypeWait type.
	TraceTypeWait TraceType = "wait"

	// TraceTypeInput type.
	TraceTypeInput TraceType = "input"

	// TraceTypeMonitor type logs the URL of the monitor that [Browser.Monitor] starts.
	TraceTypeMonitor TraceType = "monitor"
)

// ServeMonitor starts the monitor server and returns its URL.
// The reason why not to use "chrome://inspect/#devices" is one target cannot be driven by multiple controllers.
//
// The returned URL ends with a random access token path; requests outside it
// get 404 Not Found. To block DNS rebinding, the Host header must name
// localhost, a loopback IP, the listener address, or the local address the
// request connected to; other requests get 403 Forbidden. Anyone who knows the
// URL can view the pages, so keep host on loopback or protect the monitor with
// a trusted authenticated proxy. [launcher.Open] passes the URL to the browser
// as a command-line argument, which other users of the machine may be able to
// read in the process list.
func (b *Browser) ServeMonitor(host string) string {
	token := rand.Text()
	mux := http.NewServeMux()
	u, closeSvr := serve(host, func(listener net.Addr) http.Handler {
		return monitorAccess(token, listener, mux)
	})
	context.AfterFunc(b.ctx, func() {
		utils.E(closeSvr())
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		httHTML(w, assets.Monitor)
	})
	mux.HandleFunc("/api/pages", func(w http.ResponseWriter, _ *http.Request) {
		res, err := proto.TargetGetTargets{}.Call(b)
		utils.E(err)
		utils.E(requireEntries(res.TargetInfos, "TargetGetTargetsResult", "targetInfos"))

		list := []*proto.TargetTargetInfo{}
		for _, info := range res.TargetInfos {
			if info.Type == proto.TargetTargetInfoTypePage {
				list = append(list, info)
			}
		}

		w.WriteHeader(http.StatusOK)
		utils.E(w.Write(utils.MustToJSONBytes(list)))
	})
	mux.HandleFunc("/page/", func(w http.ResponseWriter, _ *http.Request) {
		httHTML(w, assets.MonitorPage)
	})
	mux.HandleFunc("/api/page/", func(w http.ResponseWriter, r *http.Request) {
		_, id, _ := strings.CutLast(r.URL.Path, "/")
		info, err := b.pageInfo(proto.TargetTargetID(id))
		utils.E(err)
		w.WriteHeader(http.StatusOK)
		utils.E(w.Write(utils.MustToJSONBytes(info)))
	})
	mux.HandleFunc("/screenshot/", func(w http.ResponseWriter, r *http.Request) {
		_, id, _ := strings.CutLast(r.URL.Path, "/")
		target := proto.TargetTargetID(id)
		p := b.MustPageFromTargetID(target)

		w.Header().Add("Content-Type", "image/png;")
		utils.E(w.Write(p.MustScreenshot()))
	})

	return u + "/" + token + "/"
}

// openMonitor opens the monitor URL in the system browser. Tests replace it.
var openMonitor = launcher.Open

// startMonitor serves the monitor for [Browser.Monitor], logs its URL, and
// tries to open the URL in the system browser.
func (b *Browser) startMonitor() {
	u := b.ServeMonitor(b.monitor)
	b.logger.Println(TraceTypeMonitor, u)
	openMonitor(u)
}

// monitorAccess serves h below the token path to requests with an allowed Host.
func monitorAccess(token string, listener net.Addr, h http.Handler) http.Handler {
	prefix := "/" + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if !monitorHostAllowed(r.Host, listener, local) {
			http.Error(w, "invalid Host header", http.StatusForbidden)
			return
		}

		segment, _, found := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if subtle.ConstantTimeCompare([]byte(segment), []byte(token)) != 1 {
			http.NotFound(w, r)
			return
		}
		if !found {
			http.Redirect(w, r, prefix+"/", http.StatusMovedPermanently)
			return
		}
		http.StripPrefix(prefix, h).ServeHTTP(w, r)
	})
}

// monitorHostAllowed reports whether a Host header names localhost, a loopback
// IP, or one of the TCP addresses. A DNS rebinding request names the
// attacker's host, which matches none of them.
func monitorHostAllowed(host string, addrs ...net.Addr) bool {
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		name, port = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), ""
	}
	if port == "" {
		port = "80"
	}
	if strings.EqualFold(strings.TrimSuffix(name, "."), "localhost") {
		return true
	}

	ip, err := netip.ParseAddr(name)
	if err != nil {
		return false
	}
	ip = ip.Unmap().WithZone("")
	if ip.IsLoopback() {
		return true
	}
	for _, addr := range addrs {
		tcp, ok := addr.(*net.TCPAddr)
		if ok && port == strconv.Itoa(tcp.Port) && ip == tcp.AddrPort().Addr().Unmap().WithZone("") {
			return true
		}
	}
	return false
}

// check method and sleep if needed.
func (b *Browser) trySlowMotion() {
	if b.slowMotion == 0 {
		return
	}

	time.Sleep(b.slowMotion)
}

// ExposeHelpers installs the helper functions in list and assigns Rod's helper
// functions to window.rod in the page's main world, so that they can be debugged
// in the DevTools console. Page scripts can then replace the helpers that Rod
// calls in that document; do not use it on untrusted pages.
func (p *Page) ExposeHelpers(list ...*js.Function) error {
	_, err := p.Evaluate(evalHelper(&js.Function{
		Name:         "_" + utils.RandString(8), // use a random name so it won't hit the cache
		Definition:   "() => { window.rod = functions }",
		Dependencies: list,
	}))
	return err
}

// Overlay a rectangle on the main frame with specified message.
// The message is displayed as plain text.
func (p *Page) Overlay(left, top, width, height float64, msg string) (remove func()) {
	return p.overlay(left, top, width, height, msg, false)
}

func (p *Page) overlay(left, top, width, height float64, msg string, monospace bool) (remove func()) {
	id := utils.RandString(8)

	_, _ = p.root.Evaluate(evalHelper(js.Overlay,
		id,
		left,
		top,
		width,
		height,
		msg,
		monospace,
	).ByPromise())

	remove = func() {
		_, _ = p.root.Evaluate(evalHelper(js.RemoveOverlay, id))
	}

	return
}

func (p *Page) tryTrace(typ TraceType, msg ...any) func() {
	if !p.browser.trace {
		return func() {}
	}

	msg = append([]any{typ}, msg...)
	msg = append(msg, p)

	p.browser.logger.Println(msg...)

	return p.Overlay(0, 0, 500, 0, fmt.Sprint(msg))
}

func (p *Page) tryTraceQuery(opts *EvalOptions) func() {
	if !p.browser.trace {
		return func() {}
	}

	p.browser.logger.Println(TraceTypeQuery, opts, p)

	return p.overlay(0, 0, 500, 0, opts.String(), true)
}

// traceText describes typed text in trace output without revealing it.
func traceText(action, text string) string {
	return fmt.Sprintf("%s (%d characters redacted)", action, utf8.RuneCountInString(text))
}

// traceKey names a key in trace output unless the key types a visible
// character or space. Control keys such as Enter and Tab stay visible.
func traceKey(action string, key input.Key) string {
	info := key.Info()
	if r, _ := utf8.DecodeRuneInString(info.Key); key.Printable() && !unicode.IsControl(r) {
		return action + " character key (redacted)"
	}
	return action + " key: " + info.Code
}

func (p *Page) tryTraceReq(includes, excludes []string) func(map[proto.NetworkRequestID]string) {
	if !p.browser.trace {
		return func(map[proto.NetworkRequestID]string) {}
	}

	msg := map[string][]string{
		"includes": includes,
		"excludes": excludes,
	}
	p.browser.logger.Println(TraceTypeWaitRequestsIdle, msg, p)
	cleanup := p.Overlay(0, 0, 500, 0, utils.MustToJSON(msg))

	ch := make(chan map[string]string)
	update := func(list map[proto.NetworkRequestID]string) {
		clone := map[string]string{}
		for k, v := range list {
			clone[string(k)] = v
		}
		select {
		case ch <- clone:
		case <-p.ctx.Done():
		}
	}

	go func() {
		var waitList map[string]string
		t := time.NewTicker(time.Second)
		for {
			select {
			case <-p.ctx.Done():
				t.Stop()
				cleanup()
				return
			case waitList = <-ch:
			case <-t.C:
				p.browser.logger.Println(TraceTypeWaitRequests, p, waitList)
			}
		}
	}()

	return update
}

// Overlay msg on the element.
// The message is displayed as plain text.
func (el *Element) Overlay(msg string) (removeOverlay func()) {
	id := utils.RandString(8)

	_, _ = el.Evaluate(evalHelper(js.ElementOverlay,
		id,
		msg,
	).ByPromise())

	removeOverlay = func() {
		_, _ = el.Evaluate(evalHelper(js.RemoveOverlay, id))
	}

	return
}

func (el *Element) tryTrace(typ TraceType, msg ...any) func() {
	if !el.page.browser.trace {
		return func() {}
	}

	msg = append([]any{typ}, msg...)
	msg = append(msg, el)

	el.page.browser.logger.Println(msg...)

	return el.Overlay(fmt.Sprint(msg))
}

func (m *Mouse) initMouseTracer() {
	_, _ = m.page.Evaluate(evalHelper(js.InitMouseTracer, m.id, assets.MousePointer).ByPromise())
}

func (m *Mouse) updateMouseTracer() bool {
	res, err := m.page.Evaluate(evalHelper(js.UpdateMouseTracer, m.id, m.pos.X, m.pos.Y))
	if err != nil {
		return true
	}
	return res.Value.Bool()
}

// Serve a port, if host is empty a random port will be used.
// The handler function receives the listener address.
func serve(host string, handler func(net.Addr) http.Handler) (string, func() error) {
	if host == "" {
		host = "127.0.0.1:0"
	}

	l, err := net.Listen("tcp", host)
	utils.E(err)

	h := handler(l.Addr())
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				utils.E(json.NewEncoder(w).Encode(err))
			}
		}()

		h.ServeHTTP(w, r)
	})}

	go func() { _ = srv.Serve(l) }()

	url := "http://" + l.Addr().String()

	return url, srv.Close
}
