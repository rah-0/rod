package rod_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

type interactiveTestFixture struct {
	page          *rod.Page
	finishParsing func()
	finishImage   func()
}

func TestPageWaitInteractiveBeforeLoad(t *testing.T) {
	g := setup(t)
	fixture := newInteractiveTestFixture(t, g)
	page := fixture.page
	interactive := startInteractiveTestWait(t, page)
	fixture.finishParsing()
	if err := <-interactive; err != nil {
		t.Fatal(err)
	}
	page.MustWait(`() => window.domReady === true`)
	if state := page.MustEval(`() => document.readyState`).Str(); state != "interactive" {
		t.Fatalf("readyState = %q, want interactive while the image is pending", state)
	}
	// A late call needs no retry, even while the image keeps load pending.
	late := page.Sleeper(func() utils.Sleeper { return utils.CountSleeper(0) })
	if returned := late.MustWaitInteractive(); returned != late {
		t.Fatal("MustWaitInteractive did not return the page for chaining")
	}
	traced, err := page.Browser().Context(page.GetContext()).Trace(true).PageFromTarget(page.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	tracedDone := make(chan error, 1)
	go func() { tracedDone <- traced.WaitInteractive() }()
	select {
	case err := <-tracedDone:
		if err != nil {
			t.Fatalf("traced parsing wait: %v", err)
		}
	case <-page.GetContext().Done():
		t.Fatal("tracing delayed WaitInteractive until full load")
	}
	loaded := make(chan error, 1)
	go func() { loaded <- page.WaitLoad() }()
	page.MustWait(`() => window.loadListenerReady === true`)
	select {
	case err := <-loaded:
		t.Fatalf("WaitLoad returned before the pending image: %v", err)
	default:
	}
	fixture.finishImage()
	if err := <-loaded; err != nil {
		t.Fatal(err)
	}
	if state := page.MustEval(`() => document.readyState`).Str(); state != "complete" {
		t.Fatalf("readyState = %q, want complete", state)
	}
	if err := late.WaitInteractive(); err != nil {
		t.Fatalf("fully loaded document needed another retry: %v", err)
	}
}

func TestPageWaitInteractiveCancellation(t *testing.T) {
	g := setup(t)
	fixture := newInteractiveTestFixture(t, g)
	page := fixture.page
	for _, end := range []string{"caller", "deadline", "session"} {
		ctx, cancel := context.WithCancel(page.GetContext())
		want := context.Canceled
		var err error
		if end == "deadline" {
			cancel()
			ctx, cancel = context.WithDeadline(page.GetContext(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
			err = page.Context(ctx).WaitInteractive()
		} else {
			waiting := startInteractiveTestWait(t, page.Context(ctx))
			if end == "caller" {
				cancel()
			} else {
				page.MustClose()
			}
			err = <-waiting
			if end == "session" && ctx.Err() != nil {
				t.Fatal("session termination canceled the caller context")
			}
		}
		cancel()
		if !errors.Is(err, want) {
			t.Fatalf("%s termination = %v, want %v", end, err, want)
		}
		if end != "session" {
			if got := page.MustEval(`() => window.interactiveListeners`).Int(); got != 0 {
				t.Fatalf("canceled wait retained %d document listeners", got)
			}
		}
	}
}

func TestPageWaitInteractiveNavigation(t *testing.T) {
	g := setup(t)
	fixture := newInteractiveTestFixture(t, g)
	page := fixture.page
	waiting := startInteractiveTestWait(t, page)
	page.MustNavigate(g.html(`<!doctype html><p>Next document</p>`))
	if err := <-waiting; err != nil {
		t.Fatalf("WaitInteractive failed across navigation: %v", err)
	}
	if got := page.MustElement("p").MustText(); got != "Next document" {
		t.Fatalf("wait returned for the wrong document: %q", got)
	}
	// A destroyed execution context can also race with the predicate request.
	g.mc.stub(1, proto.RuntimeCallFunctionOn{}, func(StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(nil), cdp.ErrCtxDestroyed
	})
	if err := page.WaitInteractive(); err != nil {
		t.Fatalf("destroyed execution context was not retried: %v", err)
	}
}

func TestPageWaitInteractiveFrameAndErrors(t *testing.T) {
	g := setup(t)
	page := g.newPage(g.html(`<iframe srcdoc="<p>Frame document</p>"></iframe>`)).Context(g.Timeout(5 * time.Second))
	frame := page.MustElement("iframe").MustFrame()
	if returned := frame.MustWaitInteractive(); returned != frame {
		t.Fatal("MustWaitInteractive did not return the frame page")
	}
	if got := frame.MustElement("p").MustText(); got != "Frame document" {
		t.Fatalf("frame content = %q", got)
	}
	injected := errors.New("interactive evaluation failed")
	g.mc.stub(1, proto.RuntimeCallFunctionOn{}, func(StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(nil), injected
	})
	panicValue := g.Panic(func() { frame.MustWaitInteractive() })
	if err, ok := panicValue.(error); !ok || !errors.Is(err, injected) {
		t.Fatalf("MustWaitInteractive panic = %v, want %v", panicValue, injected)
	}
}

func newInteractiveTestFixture(t *testing.T, g G) interactiveTestFixture {
	t.Helper()
	parsing := make(chan struct{})
	image := make(chan struct{})
	finishParsing := sync.OnceFunc(func() { close(parsing) })
	finishImage := sync.OnceFunc(func() { close(image) })
	server := g.Serve().Route("/", "", `<!doctype html><script>
window.domReady = false;
document.addEventListener('DOMContentLoaded', () => window.domReady = true, {once: true});
window.interactiveListeners = 0;
for (const target of [window, document]) {
  const add = target.addEventListener;
  const remove = target.removeEventListener;
  target.addEventListener = function(type, listener, options) {
    if (type === 'DOMContentLoaded' || type === 'readystatechange') window.interactiveListeners++;
    if (target === window && type === 'load') window.loadListenerReady = true;
    return add.call(this, type, listener, options);
  };
  target.removeEventListener = function(type, listener, options) {
    if (type === 'DOMContentLoaded' || type === 'readystatechange') window.interactiveListeners--;
    return remove.call(this, type, listener, options);
  };
}
window.interactiveFixtureReady = true;
</script><img src="/image.svg"><script src="/parsing.js"></script><p>Parsed</p>`)
	server.Mux.HandleFunc("/parsing.js", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-parsing:
			w.Header().Set("Content-Type", "application/javascript")
		case <-r.Context().Done():
		}
	})
	server.Mux.HandleFunc("/image.svg", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-image:
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`)
		case <-r.Context().Done():
		}
	})
	t.Cleanup(finishParsing)
	t.Cleanup(finishImage)
	page := g.browser.MustPage(server.URL()).Context(g.Timeout(10 * time.Second))
	page.MustWait(`() => window.interactiveFixtureReady === true`)
	if state := page.MustEval(`() => document.readyState`).Str(); state != "loading" {
		t.Fatalf("fixture readyState = %q, want loading", state)
	}
	return interactiveTestFixture{page, finishParsing, finishImage}
}

func startInteractiveTestWait(t *testing.T, page *rod.Page) <-chan error {
	t.Helper()
	started := make(chan struct{})
	var once sync.Once
	page = page.Sleeper(func() utils.Sleeper {
		sleep := utils.BackoffSleeper(time.Millisecond, 10*time.Millisecond, nil)
		return func(ctx context.Context) error {
			once.Do(func() { close(started) })
			return sleep(ctx)
		}
	})
	done := make(chan error, 1)
	go func() { done <- page.WaitInteractive() }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("WaitInteractive returned before document parsing: %v", err)
	case <-page.GetContext().Done():
		t.Fatal(page.GetContext().Err())
	}
	return done
}
