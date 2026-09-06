// Package fixture serves browser test pages on a loopback HTTP origin.
package fixture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

// ErrConfiguration indicates a missing browser or HTTP handler.
var ErrConfiguration = errors.New("fixture: browser and handler are required")

// Fixture owns a page and its HTTP server. The supplied browser remains caller-owned.
// Do not change Page or URL. Use Close to release the fixture.
type Fixture struct {
	Page *rod.Page
	URL  string

	browser *rod.Browser
	server  *http.Server
	cancel  context.CancelFunc
	done    chan struct{}
	close   sync.Once
	err     error
}

// New serves handler at a fresh loopback port and navigates a new page to its root.
// configure, when non-nil, runs before navigation, so viewport settings and event
// subscriptions can be installed before scripts execute. It must honor the page
// context. The browser must run on the same host as the server.
//
// Navigation returns after response headers; callers choose load or application
// readiness waits. Handlers own their routing and must honor request cancellation.
func New(browser *rod.Browser, handler http.Handler, configure func(*rod.Page) error) (_ *Fixture, err error) {
	if browser == nil || nilHandler(handler) {
		return nil, ErrConfiguration
	}
	if err := browser.GetContext().Err(); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fixture: listen: %w", err)
	}
	ctx, cancel := context.WithCancel(browser.GetContext())
	f := &Fixture{
		URL:     "http://" + listener.Addr().String() + "/",
		browser: browser,
		cancel:  cancel,
		done:    make(chan struct{}),
		server: &http.Server{
			Handler: handler,
			BaseContext: func(net.Listener) context.Context {
				return ctx
			},
		},
	}
	go func() {
		defer close(f.done)
		_ = f.server.Serve(listener)
	}()
	ready := false
	defer func() {
		if !ready {
			err = errors.Join(err, f.Close())
		}
	}()
	f.Page, err = browser.Context(ctx).Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("fixture: create page: %w", err)
	}
	if configure != nil {
		if err := configure(f.Page); err != nil {
			return nil, fmt.Errorf("fixture: configure page: %w", err)
		}
	}
	if err := f.Page.Navigate(f.URL); err != nil {
		return nil, fmt.Errorf("fixture: navigate: %w", err)
	}
	ready = true
	return f, nil
}

func nilHandler(handler http.Handler) bool {
	if handler == nil {
		return true
	}
	v := reflect.ValueOf(handler)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// HTML serves UTF-8 HTML at / and /index.html with caching disabled. Empty HTML
// is valid. The favicon returns 204; other paths return 404. Use New for resources
// such as relative scripts or fetch endpoints.
func HTML(browser *rod.Browser, html string, configure func(*rod.Page) error) (*Fixture, error) {
	return New(browser, htmlHandler(html), configure)
}

func htmlHandler(html string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/", "/index.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, html)
		case "/favicon.ico":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

// Close cancels handler requests, closes server connections, and forcibly closes
// the owned page without beforeunload dialogs. It has a five-second budget
// independent of the operation context and is safe to call concurrently or again.
// Handlers must release their own resources on cancellation; Close does not wait
// for arbitrary handler code. Hijacked connections remain handler-owned.
// Stop diagnostics before Close to obtain their complete final snapshot.
func (f *Fixture) Close() error {
	f.close.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(f.browser.GetContext()), 5*time.Second)
		defer cancel()
		f.cancel()
		f.err = f.server.Close()
		select {
		case <-f.done:
		case <-ctx.Done():
			f.err = errors.Join(f.err, fmt.Errorf("fixture: stop server: %w", ctx.Err()))
		}
		if f.Page != nil {
			_, err := (proto.TargetCloseTarget{TargetID: f.Page.TargetID}).Call(f.browser.Context(ctx))
			// Closing an already closed target is successful cleanup.
			if protocolErr, ok := errors.AsType[*cdp.Error](err); ok &&
				protocolErr.Code == -32602 && protocolErr.Message == "No target with given id found" {
				err = nil
			}
			if err != nil {
				f.err = errors.Join(f.err, fmt.Errorf("fixture: close page: %w", err))
			}
			f.browser.RemoveState(f.Page.TargetID)
		}
	})
	return f.err
}
