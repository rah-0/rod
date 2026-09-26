package rod_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
)

func TestBrowserBlobStreamPreservesFinalData(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	object := p.MustEvaluate(rod.Eval(`() => new Blob(["last browser bytes"])`).ByObject())
	defer p.MustRelease(object)
	blob, err := (proto.IOResolveBlob{ObjectID: object.ObjectID}).Call(p)
	if err != nil {
		t.Fatal(err)
	}
	reader := rod.NewStreamReader(p, proto.IOStreamHandle("blob:"+blob.UUID))
	defer func() { g.E(reader.Close()) }()
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "last browser bytes" {
		t.Fatalf("browser stream: %q error=%v", data, err)
	}
}

func TestPageWaitStablePreservesLaterError(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second).MustWaitLoad()
	defer p.CancelTimeout()
	injected := errors.New("DOM stability wait failed")
	g.stubDOMStableStart(func(send StubSend) (jsonvalue.Value, error) {
		// Fail after the page starts observing.
		_, _ = send()
		return jsonvalue.New(nil), injected
	})
	if err := p.WaitStable(100 * time.Millisecond); !errors.Is(err, injected) {
		t.Fatalf("WaitStable lost the later DOM wait error: %v", err)
	}
}

func TestPageWaitStableHonorsDeadline(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).MustWaitLoad()
	p.MustEval(`() => {
		let n = 0;
		window.changeDOM = setInterval(() => document.body.textContent = String(++n), 1);
	}`)
	defer p.MustEval(`() => clearInterval(window.changeDOM)`)
	limited := p.Timeout(500 * time.Millisecond)
	defer limited.CancelTimeout()
	if err := limited.WaitStable(100 * time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitStable ignored continuously changing DOM at deadline: %v", err)
	}
}

func TestPageWaitHelpersReportSetupAndCancellation(t *testing.T) {
	g := setup(t)
	for _, kind := range []string{"idle", "navigation"} {
		p := g.newPage(g.blank()).Timeout(5 * time.Second)
		injected := errors.New(kind + " setup failed")
		var request proto.Request = proto.NetworkEnable{}
		if kind == "navigation" {
			request = proto.PageSetLifecycleEventsEnabled{}
		}
		g.mc.stub(1, request, func(StubSend) (jsonvalue.Value, error) {
			return jsonvalue.New(nil), injected
		})
		var wait func() error
		if kind == "idle" {
			wait = p.WaitRequestIdle(time.Millisecond, nil, nil, nil)
		} else {
			wait = p.WaitNavigation(proto.PageLifecycleEventNameLoad)
		}
		if err := wait(); !errors.Is(err, injected) {
			t.Fatalf("%s setup failure: %v", kind, err)
		}
		p.CancelTimeout()
	}
	for _, kind := range []string{"idle", "navigation"} {
		p := g.newPage(g.blank())
		ctx, cancel := context.WithCancel(p.GetContext())
		view := p.Context(ctx)
		var wait func() error
		if kind == "idle" {
			wait = view.WaitRequestIdle(time.Hour, nil, nil, nil)
		} else {
			wait = view.WaitNavigation(proto.PageLifecycleEventNameLoad)
		}
		cancel()
		if err := wait(); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancellation: %v", kind, err)
		}
	}
}

func TestPageWaitLoadRetriesDestroyedContext(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank()).Timeout(5 * time.Second).MustWaitLoad()
	defer p.CancelTimeout()
	g.mc.stub(1, proto.RuntimeCallFunctionOn{}, func(StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(nil), cdp.ErrCtxDestroyed
	})
	if err := p.WaitLoad(); err != nil {
		t.Fatalf("WaitLoad did not retry navigation context destruction: %v", err)
	}
}

func TestPageWaitLoadSurvivesRedirectNavigation(t *testing.T) {
	g := setup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<!doctype html><img src="/pending"><script>
			const original = window.addEventListener;
			window.addEventListener = function(type, listener, options) {
				original.call(this, type, listener, options);
				if (type === 'load') window.loadListenerReady = true;
			};
			window.listenerInstrumentationReady = true;
			</script>`)
		case "/pending":
			<-r.Context().Done()
		case "/redirect":
			http.Redirect(w, r, "/next", http.StatusFound)
		case "/next":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<!doctype html><p>Next document</p>`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(func() { server.CloseClientConnections(); server.Close() })
	p := g.newPage(server.URL + "/first").Timeout(10 * time.Second)
	defer p.CancelTimeout()
	g.E(p.Wait(rod.Eval(`() => window.listenerInstrumentationReady === true`)))
	loaded := make(chan error, 1)
	go func() { loaded <- p.WaitLoad() }()
	g.E(p.Wait(rod.Eval(`() => window.loadListenerReady === true`)))
	g.E(p.Navigate(server.URL + "/redirect"))
	if err := <-loaded; err != nil {
		t.Fatalf("load wait failed across redirect navigation: %v", err)
	}
	g.Eq(p.MustElement("p").MustText(), "Next document")
}

func TestWaitWritableHonorsReadOnlyControls(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><input readonly value="input"><textarea readonly>textarea</textarea>`)).Timeout(5 * time.Second)
	defer p.CancelTimeout()
	for _, selector := range []string{"input", "textarea"} {
		control := p.MustElement(selector)
		limited := control.Timeout(150 * time.Millisecond)
		if err := limited.WaitWritable(); !errors.Is(err, context.DeadlineExceeded) {
			limited.CancelTimeout()
			t.Fatalf("read-only %s did not wait: %v", selector, err)
		}
		limited.CancelTimeout()
		control.MustEval(`() => this.readOnly = false`)
		if err := control.WaitWritable(); err != nil {
			t.Fatalf("writable %s did not become ready: %v", selector, err)
		}
	}
}
