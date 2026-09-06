package fixture

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

func TestHTMLHandler(t *testing.T) {
	for _, html := range []string{"", "<p>héllo</p>"} {
		for _, path := range []string{"/", "/index.html", "/favicon.ico", "/missing.js"} {
			r := httptest.NewRecorder()
			htmlHandler(html).ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
			if r.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store policy")
			}
			switch path {
			case "/", "/index.html":
				if r.Code != http.StatusOK || r.Body.String() != html || r.Header().Get("Content-Type") != "text/html; charset=utf-8" {
					t.Fatalf("HTML response: %d %v %q", r.Code, r.Header(), r.Body.String())
				}
			case "/favicon.ico":
				if r.Code != http.StatusNoContent || r.Body.Len() != 0 {
					t.Fatalf("favicon response: %d %q", r.Code, r.Body.String())
				}
			default:
				if r.Code != http.StatusNotFound {
					t.Fatalf("unknown path: %d", r.Code)
				}
			}
		}
	}
}

func TestInvalidConfiguration(t *testing.T) {
	var handler http.HandlerFunc
	for _, h := range []http.Handler{nil, handler, (*http.ServeMux)(nil)} {
		if f, err := New(rod.New(), h, nil); f != nil || !errors.Is(err, ErrConfiguration) {
			t.Fatalf("nil handler: %v %v", f, err)
		}
	}
	if _, err := HTML(nil, "", nil); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
}

type failureClient struct {
	events chan *cdp.Event
	call   func(context.Context, string, any) ([]byte, error)
}

func (c *failureClient) Event() <-chan *cdp.Event { return c.events }
func (c *failureClient) Call(ctx context.Context, _, method string, params any) ([]byte, error) {
	return c.call(ctx, method, params)
}

func TestSetupRollback(t *testing.T) {
	for _, stage := range []string{"create", "attach", "configure", "navigate"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cause := errors.New("setup failure")
			closed := false
			fixtureURL := ""
			client := &failureClient{events: make(chan *cdp.Event)}
			t.Cleanup(func() { close(client.events) })
			client.call = func(ctx context.Context, method string, params any) ([]byte, error) {
				switch method {
				case "Target.createTarget":
					if stage == "create" {
						return nil, cause
					}
					return []byte(`{"targetId":"fixture"}`), nil
				case "Target.attachToTarget":
					if stage == "attach" {
						cancel()
						return nil, cause
					}
					return []byte(`{"sessionId":"fixture-session"}`), nil
				case "Page.navigate":
					fixtureURL = params.(proto.PageNavigate).URL
					cancel()
					return nil, cause
				case "Target.closeTarget":
					if ctx.Err() != nil {
						t.Errorf("cleanup used canceled context: %v", ctx.Err())
					}
					closed = true
				}
				return []byte(`{}`), nil
			}
			browser := rod.New().ControlURL("").NoDefaultDevice().Context(t.Context()).Client(client)
			if err := browser.Connect(); err != nil {
				t.Fatal(err)
			}
			// Cancel the fixture operation while keeping its connection alive for rollback.
			browser = browser.Context(ctx)
			f, err := HTML(browser, "", func(*rod.Page) error {
				if fixtureURL != "" {
					t.Fatal("navigation preceded configuration")
				}
				if stage == "configure" {
					cancel()
					return cause
				}
				return nil
			})
			if f != nil || !errors.Is(err, cause) || closed != (stage != "create") {
				t.Fatalf("rollback: fixture=%v error=%v closed=%v", f, err, closed)
			}
			if fixtureURL != "" {
				response, err := http.Get(fixtureURL)
				if err == nil {
					_ = response.Body.Close()
					t.Fatal("server remained reachable after failed navigation")
				}
			}
		})
	}
}

func TestFixtureBrowser(t *testing.T) {
	browser := rod.New().ControlURL("").NoDefaultDevice()
	if err := browser.Launch(launcher.New().NoSandbox(true)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := browser.Close(); err != nil {
			t.Error(err)
		}
	})
	t.Run("origin and configuration", func(t *testing.T) {
		var diagnostics *rod.PageDiagnostics
		f, err := HTML(browser, `<script>console.log("initial fixture script");localStorage.setItem("ready", "yes");window.widthAtStart=innerWidth;indexedDB.open("fixture").onsuccess=()=>window.dbReady=true</script>`, func(page *rod.Page) error {
			var err error
			diagnostics, err = page.StartDiagnostics(rod.DiagnosticsOptions{})
			if err != nil {
				return err
			}
			return page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: 720, Height: 480, DeviceScaleFactor: 1})
		})
		if diagnostics != nil {
			defer func() { _, _ = diagnostics.Stop() }()
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { checkClose(t, f) })
		if err := f.Page.Timeout(5 * time.Second).Wait(rod.Eval(`() => window.dbReady && localStorage.getItem("ready") === "yes" && window.widthAtStart === 720`)); err != nil {
			t.Fatal(err)
		}
		snapshot, err := diagnostics.Stop()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, message := range snapshot.Console {
			found = found || strings.Contains(message.Text, "initial fixture script")
		}
		if !found {
			t.Fatalf("initial script diagnostic missing: %+v", snapshot)
		}
	})
	t.Run("handler and cancellation", func(t *testing.T) {
		started, stopped := make(chan struct{}), make(chan struct{})
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/":
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, `<script src="app.js"></script>`)
			case "/app.js":
				w.Header().Set("Content-Type", "text/javascript")
				_, _ = io.WriteString(w, `fetch("value").then(r=>r.text()).then(v=>window.value=v);fetch("slow").catch(()=>{});`)
			case "/value":
				_, _ = io.WriteString(w, "loaded")
			case "/slow":
				close(started)
				<-r.Context().Done()
				close(stopped)
			default:
				http.NotFound(w, r)
			}
		})
		ctx, cancel := context.WithCancel(browser.GetContext())
		defer cancel()
		f, err := New(browser.Context(ctx), handler, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { checkClose(t, f) })
		if err := f.Page.Timeout(5 * time.Second).Wait(rod.Eval(`() => window.value === "loaded"`)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("slow handler did not start")
		}
		cancel()
		start := time.Now()
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() { checkClose(t, f) })
		}
		wg.Wait()
		if time.Since(start) > 5*time.Second {
			t.Fatal("cleanup exceeded budget")
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("handler did not observe cancellation")
		}
		if _, err := browser.Version(); err != nil {
			t.Fatalf("fixture closed its browser: %v", err)
		}
	})
	t.Run("parallel contexts", func(t *testing.T) {
		for _, name := range []string{"one", "two"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				isolated, err := browser.Incognito()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := isolated.Close(); err != nil {
						t.Error(err)
					}
				})
				f, err := HTML(isolated, "", nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { checkClose(t, f) })
				result, err := f.Page.Eval(`value => { const old=document.cookie; document.cookie="owner="+value;localStorage.setItem("owner",value);return old }`, name)
				if err != nil || result.Value.Str() != "" {
					t.Fatalf("context inherited cookies: %v %v", result, err)
				}
				other, err := HTML(isolated, "", nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { checkClose(t, other) })
				result, err = other.Page.Eval(`() => ({cookie:document.cookie,storage:localStorage.getItem("owner")})`)
				if err != nil || !strings.Contains(result.Value.Get("cookie").Str(), "owner="+name) || !result.Value.Get("storage").Nil() {
					t.Fatalf("port origin/cookie behavior: %v %v", result, err)
				}
			})
		}
	})
}

func checkClose(t *testing.T, f *Fixture) {
	t.Helper()
	if err := f.Close(); err != nil {
		t.Error(err)
	}
}
