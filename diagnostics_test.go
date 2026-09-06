package rod_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/proto"
)

func TestPageDiagnosticsBrowser(t *testing.T) {
	g := setup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<script>
console.log('initial-console', {answer: 42}, new Map([['key', 3]]), new Set([4]));
console.error('console-only');
try { throw new Error('caught-exception'); } catch {}
Promise.reject(new Error('handled-rejection')).catch(() => {});
window.late = Promise.reject(new Error('late-rejection'));
window.resources = Promise.allSettled(['/missing','/server-error','/redirect','/broken','/blocked'].map(url => fetch(url).then(r => r.text())));
</script><script>throw new Error('uncaught-initial');</script><iframe src="/frame"></iframe>`))
		case "/frame":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<script>console.log('frame-console')</script>`))
		case "/redirect":
			http.Redirect(w, r, "/missing", http.StatusFound)
		case "/missing":
			http.Error(w, "missing", http.StatusNotFound)
		case "/server-error":
			http.Error(w, "failed", http.StatusInternalServerError)
		case "/broken":
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("short"))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	page := g.newPage()
	diagnostics, err := page.StartDiagnostics(rod.DiagnosticsOptions{})
	g.E(err)
	defer func() { _, _ = diagnostics.Stop() }()
	g.E(page.SetBlockedURLs([]string{server.URL + "/blocked"}))
	page.MustNavigate(server.URL).MustWaitLoad()
	page.MustEval(`async () => { await window.resources }`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot := diagnostics.Snapshot()
		if slices.ContainsFunc(snapshot.PageErrors, func(e rod.PageError) bool { return strings.Contains(e.Text, "late-rejection") }) && len(snapshot.ResourceFailures) >= 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing initial diagnostics: %+v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitRevoked := page.EachEvent(rod.On(func(_ *proto.RuntimeExceptionRevoked, _ proto.TargetSessionID) bool { return true }))
	page.MustEval(`() => { window.late.catch(() => {}) }`)
	waitRevoked()
	page.MustNavigate("about:blank").MustWaitLoad()
	page.MustEval(`() => { console.log('after-navigation'); window.console = {} }`)
	var world proto.RuntimeExecutionContextID
	var worldName string
	waitWorld := page.EachEvent(rod.On(func(e *proto.RuntimeExecutionContextCreated, _ proto.TargetSessionID) bool {
		if strings.HasPrefix(e.Context.Name, "__rod_diagnostics_") {
			world = e.Context.ID
			worldName = e.Context.Name
			return true
		}
		return false
	}))
	snapshot, err := diagnostics.Stop()
	g.E(err)
	waitWorld()
	if len(snapshot.PageErrors) != 1 || !strings.Contains(snapshot.PageErrors[0].Text, "uncaught-initial") || snapshot.PageErrors[0].Line == 0 {
		t.Fatalf("page errors: %+v", snapshot.PageErrors)
	}
	for _, text := range []string{"initial-console", "console-only", "frame-console", "after-navigation"} {
		if !slices.ContainsFunc(snapshot.Console, func(e rod.ConsoleMessage) bool { return strings.Contains(e.Text, text) }) {
			t.Errorf("missing console %q: %+v", text, snapshot.Console)
		}
	}
	if !slices.ContainsFunc(snapshot.Console, func(e rod.ConsoleMessage) bool {
		return strings.Contains(e.Text, `Map{"key" => 3}`) && strings.Contains(e.Text, "Set{4}")
	}) {
		t.Errorf("missing collection previews: %+v", snapshot.Console)
	}
	if !slices.ContainsFunc(snapshot.ResourceFailures, func(e rod.ResourceFailure) bool {
		return strings.HasSuffix(e.URL, "/broken") && e.Status == 500 && e.ErrorText != ""
	}) {
		t.Errorf("missing correlated body failure: %+v", snapshot.ResourceFailures)
	}
	if !slices.ContainsFunc(snapshot.ResourceFailures, func(e rod.ResourceFailure) bool { return e.BlockedReason == proto.NetworkBlockedReasonInspector }) {
		t.Errorf("missing blocked request: %+v", snapshot.ResourceFailures)
	}
	result, err := (proto.RuntimeEvaluate{ContextID: world, ReturnByValue: new(true), Expression: `Object.getOwnPropertyNames(globalThis).filter(name => name.startsWith('__rod_diagnostics_'))`}).Call(page)
	g.E(err)
	if result.ExceptionDetails != nil || len(result.Result.Value.Arr()) != 0 {
		t.Fatalf("temporary binding remained: %+v", result)
	}
	again, err := page.StartDiagnostics(rod.DiagnosticsOptions{})
	g.E(err)
	_, err = again.Stop()
	g.E(err)
	reused, err := (proto.PageCreateIsolatedWorld{FrameID: page.FrameID, WorldName: worldName}).Call(page)
	g.E(err)
	if reused.ExecutionContextID != world {
		t.Fatal("repeated collection created another isolated world")
	}

}

func TestPageDiagnosticsBrowserCancellation(t *testing.T) {
	g := setup(t)
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/waiting" {
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html>"))
	}))
	defer server.Close()
	page := g.browser.MustPage(server.URL)
	defer func() { _ = page.Close() }()
	diagnostics, err := page.StartDiagnostics(rod.DiagnosticsOptions{})
	g.E(err)
	defer func() { _, _ = diagnostics.Stop() }()
	waitCanceled := page.EachEvent(rod.On(func(e *proto.NetworkLoadingFailed, _ proto.TargetSessionID) bool { return e.Canceled }))
	page.MustEval(`() => { window.controller = new AbortController(); fetch('/waiting', {signal: controller.signal}).catch(() => {}) }`)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	page.MustEval(`() => { controller.abort(); console.log('before-close') }`)
	waitCanceled()
	// A successful Stop preserves the cancellation flag without imposing policy.
	snapshot, err := diagnostics.Stop()
	g.E(err)
	if !slices.ContainsFunc(snapshot.ResourceFailures, func(e rod.ResourceFailure) bool { return e.Canceled && strings.HasSuffix(e.URL, "/waiting") }) {
		t.Fatalf("missing cancellation: %+v", snapshot)
	}
	closed, err := page.StartDiagnostics(rod.DiagnosticsOptions{})
	g.E(err)
	page.MustEval(`() => console.log('retained-before-close')`)
	deadline := time.Now().Add(5 * time.Second)
	for len(closed.Snapshot().Console) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	g.E(page.Close())
	snapshot, err = closed.Stop()
	if !errors.Is(err, rod.ErrDiagnosticsIncomplete) || len(snapshot.Console) == 0 {
		t.Fatalf("closed target: %+v, %v", snapshot, err)
	}
}

func TestPageDiagnosticsBrowserExpiredContext(t *testing.T) {
	g := setup(t)
	page := g.newPage()
	ctx, cancel := context.WithCancel(page.GetContext())
	diagnostics, err := page.Context(ctx).StartDiagnostics(rod.DiagnosticsOptions{})
	g.E(err)
	cancel()
	_, err = diagnostics.Stop()
	if !errors.Is(err, rod.ErrDiagnosticsIncomplete) {
		t.Fatalf("expired context: %v", err)
	}
}

func TestPageDiagnosticsBrowserFrameCleanup(t *testing.T) {
	g := setup(t)
	page := g.newPage(g.html(`<iframe srcdoc="<p>first</p>"></iframe><iframe srcdoc="<p>second</p>"></iframe>`)).MustWaitLoad()
	elements := page.MustElements("iframe")
	frames := []*rod.Page{elements[0].MustFrame(), elements[1].MustFrame()}
	collectors := make([]*rod.PageDiagnostics, len(frames))
	for i, frame := range frames {
		if frame.SessionID != page.SessionID {
			t.Fatal("fixture must use same-session frames")
		}
		var err error
		collectors[i], err = frame.StartDiagnostics(rod.DiagnosticsOptions{})
		g.E(err)
		defer func() { _, _ = collectors[i].Stop() }()
	}
	worlds := make(map[proto.PageFrameID]*proto.RuntimeExecutionContextDescription)
	observeCtx, observeCancel := context.WithTimeout(page.GetContext(), 5*time.Second)
	defer observeCancel()
	waitWorlds := page.Context(observeCtx).EachEvent(rod.On(func(e *proto.RuntimeExecutionContextCreated, _ proto.TargetSessionID) bool {
		if !strings.HasPrefix(e.Context.Name, "__rod_diagnostics_") {
			return false
		}
		frameID := proto.PageFrameID(e.Context.AuxData["frameId"].Str())
		if frameID == frames[0].FrameID || frameID == frames[1].FrameID {
			worlds[frameID] = e.Context
		}
		return len(worlds) == len(frames)
	}))
	results := make(chan error, len(collectors))
	for _, collector := range collectors {
		go func() { _, err := collector.Stop(); results <- err }()
	}
	for range collectors {
		g.E(<-results)
	}
	waitWorlds()
	if len(worlds) != len(frames) {
		t.Fatalf("missing frame worlds: %+v", worlds)
	}
	if worlds[frames[0].FrameID].Name == worlds[frames[1].FrameID].Name {
		t.Error("binding world names overlap across frames")
	}
	for frameID, world := range worlds {
		result, err := (proto.RuntimeEvaluate{
			ContextID: world.ID, ReturnByValue: new(true),
			Expression: `Object.getOwnPropertyNames(globalThis).filter(name => name.startsWith('__rod_diagnostics_'))`,
		}).Call(page)
		g.E(err)
		if result.ExceptionDetails != nil || len(result.Result.Value.Arr()) != 0 {
			t.Errorf("temporary binding remained in frame %s: %+v", frameID, result)
		}
	}
}
