package rod_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/devices"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

func TestIncognito(t *testing.T) {
	g := setup(t)

	k := g.RandStr(16)

	b := g.browser.MustIncognito().Sleeper(rod.DefaultSleeper)
	defer b.MustClose()

	page := b.MustPage(g.blank())
	defer page.MustClose()
	page.MustEval(`k => localStorage[k] = 1`, k)

	g.True(g.page.MustNavigate(g.blank()).MustEval(`k => localStorage[k]`, k).Nil())
	g.Eq(page.MustEval(`k => localStorage[k]`, k).Str(), "1") // localStorage can only store string

	g.Panic(func() {
		g.mc.stubErr(1, proto.TargetCreateBrowserContext{})
		g.browser.MustIncognito()
	})
}

func TestDefaultDevice(t *testing.T) {
	g := setup(t)

	ua := ""

	s := g.Serve()
	s.Mux.HandleFunc("/t", func(_ http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
	})

	// TODO: https://github.com/golang/go/issues/51459
	b := *g.browser
	b.DefaultDevice(devices.IPhoneX)

	b.MustPage(s.URL("/t")).MustClose()
	g.Eq(ua, devices.IPhoneX.UserAgentEmulation().UserAgent)

	b.NoDefaultDevice()
	b.MustPage(s.URL("/t")).MustClose()
	g.Neq(ua, devices.IPhoneX.UserAgentEmulation().UserAgent)
}

func TestPageErr(t *testing.T) {
	g := setup(t)

	g.Panic(func() {
		g.mc.stubErr(1, proto.TargetAttachToTarget{})
		g.browser.MustPage()
	})
}

func TestPageFromTarget(t *testing.T) {
	g := setup(t)

	g.Panic(func() {
		res, err := proto.TargetCreateTarget{URL: "about:blank"}.Call(g.browser)
		g.E(err)
		defer func() {
			g.browser.MustPageFromTargetID(res.TargetID).MustClose()
		}()

		g.mc.stubErr(1, proto.EmulationSetDeviceMetricsOverride{})
		g.browser.MustPageFromTargetID(res.TargetID)
	})
}

func TestBrowserPages(t *testing.T) {
	g := setup(t)

	b := g.browser
	pages := b.MustPages()
	g.Gte(len(pages), 1)

	{
		g.mc.stub(1, proto.TargetGetTargets{}, func(send StubSend) (jsonvalue.Value, error) {
			d, _ := send()
			return *d.Set("targetInfos.0.type", "iframe"), nil
		})
		b.MustPages()
	}

	g.Panic(func() {
		g.mc.stubErr(1, proto.TargetCreateTarget{})
		b.MustPage()
	})
	g.Panic(func() {
		g.mc.stubErr(1, proto.TargetGetTargets{})
		b.MustPages()
	})
	g.Panic(func() {
		_, err := proto.TargetCreateTarget{URL: "about:blank"}.Call(b)
		g.E(err)
		g.mc.stubErr(1, proto.TargetAttachToTarget{})
		b.MustPages()
	})
}

func TestBrowserClearStates(t *testing.T) {
	g := setup(t)

	g.E(proto.EmulationClearGeolocationOverride{}.Call(g.page))
}

func TestBrowserEvent(t *testing.T) {
	g := setup(t)

	messages := g.browser.Context(g.Context()).Event()
	p := g.newPage()
	wait := make(chan struct{})
	for msg := range messages {
		e := proto.TargetAttachedToTarget{}
		if msg.Load(&e) {
			g.Eq(e.TargetInfo.TargetID, p.TargetID)
			close(wait)
			break
		}
	}
	<-wait
}

func TestBrowserWaitEvent(t *testing.T) {
	g := setup(t)

	g.NotNil(g.browser.Context(g.Context()).Event())

	wait := g.page.WaitEvent(&proto.PageFrameNavigated{})
	g.page.MustNavigate(g.blank())
	wait()

	wait = g.browser.EachEvent(rod.On(func(_ *proto.PageFrameNavigated, _ proto.TargetSessionID) bool {
		return true
	}))
	g.page.MustNavigate(g.blank())
	wait()
}

func TestBrowserCrash(t *testing.T) {
	g := setup(t)

	browser := rod.New().NoDefaultDevice().Context(g.Context()).MustConnect()
	page := browser.MustPage().Timeout(10 * time.Second)
	defer page.CancelTimeout()
	started := page.EachEvent(rod.On(func(event *proto.RuntimeConsoleAPICalled, _ proto.TargetSessionID) bool {
		return len(event.Args) == 1 && event.Args[0].Value.Str() == "pending-evaluation"
	}))
	pending := make(chan error, 1)
	go func() {
		_, err := page.Eval(`() => new Promise(() => console.log("pending-evaluation"))`)
		pending <- err
	}()
	g.E(started()) // The evaluation is awaiting its promise when the browser crashes.
	events := page.Event()
	_ = proto.BrowserCrash{}.Call(browser.Context(page.GetContext()))
	for range events {
	}
	g.NotNil(<-pending)
	g.E(page.GetContext().Err()) // Session loss does not cancel the caller's context.
	_, err := page.Eval(`() => 1`)
	g.True(errors.Is(err, context.Canceled))
}

func TestBrowserCall(t *testing.T) {
	g := setup(t)

	v, err := proto.BrowserGetVersion{}.Call(g.browser)
	g.E(err)

	g.Regex("1.3", v.ProtocolVersion)
}

func TestBlockingNavigation(t *testing.T) {
	g := setup(t)

	// One pending navigation must not prevent another page from loading.
	s := g.Serve()
	pause := g.Context()
	entered := make(chan struct{})
	s.Mux.HandleFunc("/a", func(_ http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-pause.Done()
	})
	s.Route("/b", ".html", `<html>ok</html>`)

	ctx := g.Context()
	blocked := g.newPage().Context(ctx)
	done := make(chan error, 1)
	go func() { done <- blocked.Navigate(s.URL("/a")) }()
	<-entered

	g.Eq(g.newPage(s.URL("/b")).MustElement("html").MustText(), "ok")
	select {
	case err := <-done:
		t.Fatalf("blocked navigation returned before cancellation: %v", err)
	default:
	}
	ctx.Cancel()
	g.True(errors.Is(<-done, context.Canceled))
}

func TestResolveBlocking(t *testing.T) {
	g := setup(t)

	s := g.Serve()

	pause := g.Context()

	s.Mux.HandleFunc("/", func(_ http.ResponseWriter, _ *http.Request) {
		<-pause.Done()
	})

	p := g.newPage()

	go func() {
		utils.Sleep(0.1)
		p.MustStopLoading()
	}()

	g.Panic(func() {
		p.MustNavigate(s.URL())
	})
}

func TestBrowserOthers(t *testing.T) {
	g := setup(t)

	g.browser.Timeout(time.Second).CancelTimeout().MustGetCookies()
}

func TestBinarySize(t *testing.T) {
	g := testutil.New(t)

	if runtime.GOOS == "windows" || utils.InContainer {
		g.SkipNow()
	}

	binary := filepath.Join(t.TempDir(), "owned-launch")
	cmd := exec.Command("go", "build",
		"-trimpath",
		"-ldflags", "-w -s",
		"-o", binary,
		"./examples/owned-launch")

	cmd.Dir = repoPath(".")
	cmd.Env = append(os.Environ(), "GOOS=linux")

	g.Nil(cmd.Run())

	stat, err := os.Stat(binary)
	g.E(err)

	g.Lte(float64(stat.Size())/1024/1024, 11) // mb
}

func TestBrowserCookies(t *testing.T) {
	g := setup(t)

	b := g.browser.MustIncognito()
	defer b.MustClose()

	b.MustSetCookies(&proto.NetworkCookie{
		Name:   "a",
		Value:  "val",
		Domain: "test.com",
	})

	cookies := b.MustGetCookies()

	g.Len(cookies, 1)
	g.Eq(cookies[0].Name, "a")
	g.Eq(cookies[0].Value, "val")

	{
		b.MustSetCookies()
		cookies := b.MustGetCookies()
		g.Len(cookies, 0)
	}

	g.mc.stubErr(1, proto.StorageGetCookies{})
	g.Err(b.GetCookies())
}

func TestWaitDownload(t *testing.T) {
	g := setup(t)

	s := g.Serve()
	content := "test content"

	s.Route("/d", ".bin", []byte(content))
	s.Route("/page", ".html", fmt.Sprintf(`<html><a href="%s/d" download>click</a></html>`, s.URL()))

	page := g.page.MustNavigate(s.URL("/page"))

	wait := g.browser.MustWaitDownload()
	page.MustElement("a").MustClick()
	data := wait()

	g.Eq(content, string(data))
}

func TestWaitDownloadDataURI(t *testing.T) {
	g := setup(t)

	s := g.Serve()

	s.Route("/", ".html",
		`<html>
			<a id="a" href="data:text/plain;,test%20data" download>click</a>
			<a id="b" download>click</a>
			<script>
				const b = document.getElementById('b')
				b.href = URL.createObjectURL(new Blob(['test blob'], {
					type: "text/plain; charset=utf-8"
				}))
			</script>
		</html>`,
	)

	page := g.page.MustNavigate(s.URL())

	wait1 := g.browser.MustWaitDownload()
	page.MustElement("#a").MustClick()
	data := wait1()
	g.Eq("test data", string(data))

	wait2 := g.browser.MustWaitDownload()
	page.MustElement("#b").MustClick()
	data = wait2()
	g.Eq("test blob", string(data))
}

func TestWaitDownloadCancel(t *testing.T) {
	g := setup(t)

	wait, err := g.browser.Context(g.Timeout(0)).WaitDownload(os.TempDir())
	g.Nil(wait)
	g.Is(err, context.DeadlineExceeded)
}

func TestWaitDownloadFromNewPage(t *testing.T) {
	g := setup(t)

	s := g.Serve()
	content := "test content"

	s.Route("/d", ".bin", content)
	s.Route("/page", ".html", fmt.Sprintf(
		`<html><a href="%s/d" download target="_blank">click</a></html>`,
		s.URL()),
	)

	page := g.page.MustNavigate(s.URL("/page"))
	wait := g.browser.MustWaitDownload()
	page.MustElement("a").MustClick()
	data := wait()

	g.Eq(content, string(data))
}

func TestBrowserConnectErr(t *testing.T) {
	g := setup(t)

	g.Panic(func() {
		rod.New().ControlURL(g.RandStr(16)).MustConnect()
	})
}

func TestStreamReader(t *testing.T) {
	g := setup(t)

	r := rod.NewStreamReader(g.page, "")
	n, err := r.Read(nil)
	g.E(err)
	g.Eq(n, 0)

	g.mc.stub(1, proto.IORead{}, func(_ StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(proto.IOReadResult{
			Data: "test",
		}), nil
	})
	b := make([]byte, 4)
	_, _ = r.Read(b)
	g.Eq("test", string(b))

	g.mc.stubErr(1, proto.IORead{})
	_, err = r.Read(b)
	g.Err(err)

	g.mc.stub(1, proto.IORead{}, func(_ StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(proto.IOReadResult{
			Base64Encoded: true,
			Data:          "@",
		}), nil
	})
	_, err = r.Read(b)
	g.Err(err)
}

func TestBrowserPool(t *testing.T) {
	g := testutil.T(t)

	pool := rod.NewBrowserPool(3)
	t.Cleanup(func() {
		pool.Cleanup(func(p *rod.Browser) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.Context(ctx).Close(); err != nil {
				t.Errorf("close pooled browser: %v", err)
			}
		})
	})

	b, err := pool.Get(func() (*rod.Browser, error) {
		browser := rod.New()
		return browser, browser.Connect()
	})
	g.E(err)
	pool.Put(b)

	b = pool.MustGet(func() *rod.Browser { return rod.New().MustConnect() })
	pool.Put(b)
}

func TestBrowserLostConnection(t *testing.T) {
	g := setup(t)

	l := launcher.New()
	defer func() {
		l.Kill()
		l.Cleanup()
	}()
	u := l.MustLaunch()

	p := rod.New().ControlURL(u).MustConnect().MustPage(g.blank())

	go func() {
		utils.Sleep(1)
		l.Kill()
	}()

	_, err := p.Eval(`() => new Promise(r => {})`)
	g.Err(err)
}
