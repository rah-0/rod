package rod_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

func TestHijack(t *testing.T) {
	g := setup(t)

	s := g.Serve()

	// to simulate a backend server
	s.Route("/", repoPath("fixtures/fetch.html"))
	s.Mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			panic("wrong http method")
		}

		g.Eq("header", r.Header.Get("Test"))

		b, err := io.ReadAll(r.Body)
		g.E(err)
		g.Eq("a", string(b))

		g.HandleHTTP(".html", "test")(w, r)
	})
	s.Route("/b", "", "b")

	router := g.page.HijackRequests()
	defer router.MustStop()

	router.MustAdd(s.URL("/a"), func(ctx *rod.Hijack) {
		r := ctx.Request.SetContext(g.Context())
		original := r.Body()
		r.Req().Header.Set("Test", "header") // override request header
		r.SetBody([]byte("test"))            // override request body
		r.SetBody(123)                       // override request body
		g.Eq(r.Body(), "123")
		r.SetBody(original) // restore the captured body

		type MyState struct {
			Val int
		}

		ctx.CustomState = &MyState{10}

		g.Eq(http.MethodPost, r.Method())
		g.Eq(s.URL("/a"), r.URL().String())

		g.Eq(proto.NetworkResourceTypeXHR, ctx.Request.Type())
		g.Is(ctx.Request.IsNavigation(), false)
		g.Has(s.URL(), ctx.Request.Header("Origin"))
		g.Eq(ctx.Request.Headers()["Test"].String(), "header")
		g.True(ctx.Request.JSONBody().Nil())

		// send request load response from real destination as the default value to hijack
		ctx.MustLoadResponse()

		g.Eq(200, ctx.Response.Payload().ResponseCode)

		// override status code
		ctx.Response.Payload().ResponseCode = http.StatusCreated

		g.Eq("4", ctx.Response.Headers().Get("Content-Length"))
		g.Has(ctx.Response.Headers().Get("Content-Type"), "text/html; charset=utf-8")

		// override response header
		ctx.Response.AddHeader("Set-Cookie", "key=val1")
		// This should override the previous one
		ctx.Response.SetHeader("Set-Cookie", "key=val")

		// override response body
		ctx.Response.SetBody([]byte("test"))
		ctx.Response.SetBody("test")
		ctx.Response.SetBody(map[string]string{
			"text": "test",
		})

		g.Eq("{\"text\":\"test\"}", ctx.Response.Body())
	})

	router.MustAdd(s.URL("/b"), func(_ *rod.Hijack) {
		panic("should not come to here")
	})
	router.MustRemove(s.URL("/b"))

	router.MustAdd(s.URL("/b"), func(ctx *rod.Hijack) {
		// transparent proxy
		ctx.MustLoadResponse()
	})

	go router.Run()

	g.page.MustNavigate(s.URL())

	g.Eq("201 test key=val", g.page.MustElement("#a").MustText())
	g.Eq("b", g.page.MustElement("#b").MustText())
}

func TestHijackContinue(t *testing.T) {
	g := setup(t)

	s := g.Serve().Route("/", ".html", `<body>ok</body>`)

	router := g.page.HijackRequests()
	defer router.MustStop()

	wg := &sync.WaitGroup{}
	wg.Add(1)
	router.MustAdd(s.URL("/a"), func(ctx *rod.Hijack) {
		ctx.ContinueRequest(&proto.FetchContinueRequest{})
		wg.Done()
	})

	go router.Run()

	g.page.MustNavigate(s.URL("/a"))

	g.Eq("ok", g.page.MustElement("body").MustText())
	wg.Wait()
}

func TestHijackMockWholeResponseEmptyBody(t *testing.T) {
	g := setup(t)

	router := g.page.HijackRequests()
	defer router.MustStop()

	router.MustAdd("*", func(ctx *rod.Hijack) {
		ctx.Response.SetBody("")
	})

	go router.Run()

	// needs to timeout or will hang when "omitempty" does not get removed from body in fulfillRequest
	timed := g.page.Timeout(time.Second)
	timed.MustNavigate(g.Serve().Route("/", ".txt", "OK").URL())

	g.Eq("", g.page.MustElement("body").MustText())
}

func TestHijackMockWholeResponseNoBody(t *testing.T) {
	// TODO: remove the skip
	t.Skip("Because of flaky test result")

	g := setup(t)

	router := g.page.HijackRequests()
	defer router.MustStop()

	// intercept and reply without setting a body
	router.MustAdd("*", func(_ *rod.Hijack) {
		// we don't set any body here
	})

	go router.Run()

	// has to timeout as it will lock up the browser reading the reply.
	err := g.page.Timeout(time.Second).Navigate(g.Serve().Route("/", "").URL())
	g.Is(err, context.DeadlineExceeded)
}

func TestHijackMockWholeResponse(t *testing.T) {
	g := setup(t)

	router := g.page.HijackRequests()
	defer router.MustStop()

	router.MustAdd("*", func(ctx *rod.Hijack) {
		ctx.Response.SetHeader("Content-Type", mime.TypeByExtension(".html"))
		ctx.Response.SetBody("<body>ok</body>")
	})

	go router.Run()

	g.page.MustNavigate("http://localhost")

	g.Eq("ok", g.page.MustElement("body").MustText())
}

func TestHijackSkip(t *testing.T) {
	g := setup(t)

	s := g.Serve()

	router := g.page.HijackRequests()
	defer router.MustStop()

	wg := &sync.WaitGroup{}
	wg.Add(2)
	router.MustAdd(s.URL("/a"), func(ctx *rod.Hijack) {
		ctx.Skip = true
		wg.Done()
	})
	router.MustAdd(s.URL("/a"), func(ctx *rod.Hijack) {
		ctx.ContinueRequest(&proto.FetchContinueRequest{})
		wg.Done()
	})

	go router.Run()

	g.page.MustNavigate(s.URL("/a"))

	wg.Wait()
}

func TestHijackOnErrorLog(t *testing.T) {
	g := setup(t)

	s := g.Serve().Route("/", ".html", `<body>ok</body>`)

	router := g.page.HijackRequests()
	defer router.MustStop()

	wg := &sync.WaitGroup{}
	wg.Add(1)
	var err error

	router.MustAdd(s.URL("/a"), func(ctx *rod.Hijack) {
		ctx.OnError = func(e error) {
			err = e
			wg.Done()
		}
		ctx.ContinueRequest(&proto.FetchContinueRequest{})
	})

	go router.Run()

	g.mc.stub(1, proto.FetchContinueRequest{}, func(_ StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(nil), errors.New("err")
	})

	go func() {
		_ = g.page.Context(g.Context()).Navigate(s.URL("/a"))
	}()
	wg.Wait()

	g.Eq(err.Error(), "err")
}

func TestHijackFailRequest(t *testing.T) {
	g := setup(t)

	s := g.Serve().Route("/page", ".html", `<html>
	<body></body>
	<script>
		fetch('/a').catch(async (err) => {
			document.title = err.message
		})
	</script></html>`)

	router := g.browser.HijackRequests()
	defer router.MustStop()

	router.MustAdd(s.URL("/a"), func(ctx *rod.Hijack) {
		ctx.Response.Fail(proto.NetworkErrorReasonAborted)
	})

	go router.Run()

	g.page.MustNavigate(s.URL("/page")).MustWaitLoad()

	g.page.MustWait(`() => document.title === 'Failed to fetch'`)

	{ // test error log
		g.mc.stub(1, proto.FetchFailRequest{}, func(send StubSend) (jsonvalue.Value, error) {
			_, _ = send()
			return jsonvalue.Value{}, errors.New("err")
		})
		_ = g.page.Navigate(s.URL("/a"))
	}
}

func TestHijackLoadResponseErr(t *testing.T) {
	g := setup(t)

	p := g.newPage().Context(g.Context())
	router := p.HijackRequests()
	defer router.MustStop()

	wg := &sync.WaitGroup{}
	wg.Add(1)

	router.MustAdd("http://localhost/a", func(ctx *rod.Hijack) {
		g.Err(ctx.LoadResponse(&http.Client{
			Transport: &MockRoundTripper{err: errors.New("err")},
		}, true))

		g.Err(ctx.LoadResponse(&http.Client{
			Transport: &MockRoundTripper{res: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(&MockReader{err: errors.New("err")}),
			}},
		}, true))

		wg.Done()

		ctx.Response.Fail(proto.NetworkErrorReasonAborted)
	})

	go router.Run()

	_ = p.Navigate("http://localhost/a")

	wg.Wait()
}

func TestHijackResponseErr(t *testing.T) {
	g := setup(t)

	s := g.Serve().Route("/", ".html", `ok`)

	p := g.newPage().Context(g.Context())
	router := p.HijackRequests()
	defer router.MustStop()

	wg := &sync.WaitGroup{}
	wg.Add(1)

	router.MustAdd(s.URL("/a"), func(ctx *rod.Hijack) { // to ignore favicon
		ctx.OnError = func(err error) {
			g.Err(err)
			wg.Done()
		}

		ctx.MustLoadResponse()
		g.mc.stub(1, proto.FetchFulfillRequest{}, func(send StubSend) (jsonvalue.Value, error) {
			res, _ := send()
			return res, errors.New("err")
		})
	})

	go router.Run()

	p.MustNavigate(s.URL("/a"))

	wg.Wait()
}

func TestHandleAuth(t *testing.T) {
	g := setup(t)

	s := g.Serve()

	// mock the server
	s.Mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok {
			w.Header().Add("WWW-Authenticate", `Basic realm="web"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		g.Eq("a", u)
		g.Eq("b", p)
		g.HandleHTTP(".html", `<p>ok</p>`)(w, r)
	})
	s.Route("/err", ".html", "err page")

	credentials := rod.AuthCredentials{Source: proto.FetchAuthChallengeSourceServer, Origin: s.URL(), Username: "a", Password: "b"}
	go g.browser.MustHandleAuth(credentials)()

	page := g.newPage(s.URL("/a"))
	page.MustElementR("p", "ok")

	wait := g.browser.HandleAuth(credentials)
	var page2 *rod.Page
	wait2 := utils.All(func() {
		page2, _ = g.browser.Page(proto.TargetCreateTarget{URL: s.URL("/err")})
	})
	g.mc.stubErr(1, proto.FetchContinueRequest{})
	g.Err(wait())
	wait2()
	page2.MustClose()
}

func TestHandleAuthScopedCredentials(t *testing.T) {
	g := setup(t)

	var authorized atomic.Int32
	s := g.Serve()
	s.Mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authorized.Add(1)
		}
		w.Header().Add("WWW-Authenticate", `Basic realm="web"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	other := g.Serve()
	page := g.newPage()

	for _, credentials := range []rod.AuthCredentials{
		// Proxy credentials must not answer a server challenge from the same address.
		{Source: proto.FetchAuthChallengeSourceProxy, Origin: s.URL(), Username: "a", Password: "b"},
		{Source: proto.FetchAuthChallengeSourceServer, Origin: other.URL(), Username: "a", Password: "b"},
		// Proxy credentials for any origin still do not answer a server challenge.
		{Source: proto.FetchAuthChallengeSourceProxy, AnyOrigin: true, Username: "a", Password: "b"},
	} {
		ctx, cancel := context.WithCancel(g.Context())
		wait := g.browser.Context(ctx).HandleAuth(credentials)
		done := make(chan error, 1)
		go func() { done <- wait() }()

		err := page.Navigate(s.URL("/a"))
		g.Is(err, &rod.NavigationError{})
		g.Has(err.Error(), "net::ERR_INVALID_AUTH_CREDENTIALS")

		cancel()
		g.Is(<-done, context.Canceled)
	}
	g.Eq(authorized.Load(), int32(0))
}

func TestHandleAuthAnyOriginBrowser(t *testing.T) {
	g := setup(t)

	s := g.Serve()
	s.Mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "a" || p != "b" {
			w.Header().Add("WWW-Authenticate", `Basic realm="web"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		g.HandleHTTP(".html", `<p>ok</p>`)(w, r)
	})

	// The credentials name no origin, so the first server challenge receives them.
	wait := g.browser.HandleAuth(rod.AuthCredentials{
		Source: proto.FetchAuthChallengeSourceServer, AnyOrigin: true, Username: "a", Password: "b",
	})
	done := make(chan error, 1)
	go func() { done <- wait() }()

	page := g.newPage(s.URL("/a"))
	page.MustElementR("p", "ok")
	g.E(<-done)
}

func TestHijackHandlerPanicFailsRequest(t *testing.T) {
	g := setup(t)

	s := g.Serve().Route("/", ".html", `<body>ok</body>`)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	g.E(err)
	refused := "http://" + listener.Addr().String()
	g.E(listener.Close())

	router := g.page.HijackRequests()
	defer router.MustStop()

	errs := make(chan error, 8)
	router.MustAdd(refused+"/*", func(ctx *rod.Hijack) {
		ctx.OnError = func(err error) { errs <- err }
		ctx.MustLoadResponse() // panics with the connection error
	})

	go router.Run()

	g.page.MustNavigate(s.URL()).MustWaitLoad()
	g.Eq(g.page.MustEval(`async (u) => {
		try { await fetch(u); return "loaded" } catch (e) { return e.name }
	}`, refused+"/api").Str(), "TypeError")

	err = <-errs
	g.Is(err, &rod.TryError{})
	g.Is(err, syscall.ECONNREFUSED)
}

func TestHijackUnparsableURLFails(t *testing.T) {
	g := setup(t)

	s := g.Serve().Route("/", ".html", `<body>ok</body>`)

	router := g.page.HijackRequests()
	defer router.MustStop()

	errs := make(chan error, 8)
	router.OnError(func(err error) { errs <- err })
	var handled atomic.Int32
	router.MustAdd(s.URL("/x*"), func(ctx *rod.Hijack) {
		handled.Add(1)
		ctx.Response.SetBody(ctx.Request.URL().Path)
	})

	go router.Run()

	g.page.MustNavigate(s.URL()).MustWaitLoad()
	fetchText := `async (u) => { try { return await (await fetch(u)).text() } catch (e) { return e.name } }`
	// Chrome keeps the invalid escape, which net/url rejects.
	g.Eq(g.page.MustEval(fetchText, "/x%zz").Str(), "TypeError")
	_, ok := errors.AsType[*url.Error](<-errs)
	g.True(ok)
	g.Eq(g.page.MustEval(fetchText, "/x%41").Str(), "/xA")
	g.Eq(handled.Load(), int32(1))
}

func TestHijackLoadResponseCrossOriginRedirect(t *testing.T) {
	g := setup(t)

	internal := g.Serve().Route("/secret", ".txt", "internal secret")
	front := g.Serve().Route("/", ".html", `<body>front</body>`).Route("/final", ".txt", "same origin")
	front.Mux.HandleFunc("/leak", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL("/secret"), http.StatusFound)
	})
	front.Mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})

	router := g.page.HijackRequests()
	defer router.MustStop()

	router.MustAdd("*", func(ctx *rod.Hijack) {
		ctx.MustLoadResponse()
	})

	go router.Run()

	g.page.MustNavigate(front.URL()).MustWaitLoad()
	// The browser follows the redirect, so CORS protects the other origin's response.
	g.Eq(g.page.MustEval(`async () => {
		try { return await (await fetch("/leak")).text() } catch (e) { return e.name }
	}`).Str(), "TypeError")

	var hop struct {
		Body       string
		Redirected bool
		URL        string
	}
	g.E(g.page.EvalJSON(&hop, `async () => {
		const res = await fetch("/hop");
		return {Body: await res.text(), Redirected: res.redirected, URL: res.url};
	}`))
	g.Eq(hop.Body, "same origin")
	g.True(hop.Redirected)
	g.Eq(hop.URL, front.URL("/final"))
}

func TestHijackLoadResponseFollowRedirectsBrowser(t *testing.T) {
	g := setup(t)

	internal := g.Serve().Route("/secret", ".txt", "internal secret")
	front := g.Serve().Route("/", ".html", `<body>front</body>`).Route("/final", ".txt", "same origin")
	front.Mux.HandleFunc("/leak", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL("/secret"), http.StatusFound)
	})
	front.Mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})

	router := g.page.HijackRequests()
	defer router.MustStop()

	router.MustAdd("*", func(ctx *rod.Hijack) {
		ctx.FollowRedirects = true
		ctx.MustLoadResponse()
	})

	go router.Run()

	g.page.MustNavigate(front.URL()).MustWaitLoad()
	// Go follows the redirect, so the page reads the other origin's response as its own.
	g.Eq(g.page.MustEval(`async () => {
		try { return await (await fetch("/leak")).text() } catch (e) { return e.name }
	}`).Str(), "internal secret")

	var hop struct {
		Body       string
		Redirected bool
		URL        string
	}
	g.E(g.page.EvalJSON(&hop, `async () => {
		const res = await fetch("/hop");
		return {Body: await res.text(), Redirected: res.redirected, URL: res.url};
	}`))
	g.Eq(hop.Body, "same origin")
	g.False(hop.Redirected)
	g.Eq(hop.URL, front.URL("/hop"))
}

func TestHijackLoadResponseHeaderLimitBrowser(t *testing.T) {
	g := setup(t)

	s := g.Serve().Route("/", ".html", `<body>ok</body>`)
	// Chrome rejects headers this large from the network, but not when fulfilled.
	big := strings.Repeat("a", 300<<10)
	s.Mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Big", big)
		_, _ = io.WriteString(w, "body")
	})

	router := g.page.HijackRequests()
	defer router.MustStop()

	errs := make(chan error, 8)
	router.OnError(func(err error) { errs <- err })
	router.MustAdd(s.URL("/big*"), func(ctx *rod.Hijack) {
		if ctx.Request.URL().Query().Has("raised") {
			ctx.MaxResponseHeaderBytes = 1 << 20
		}
		ctx.MustLoadResponse()
	})

	go router.Run()

	g.page.MustNavigate(s.URL()).MustWaitLoad()
	fetchText := `async (u) => {
		try {
			const res = await fetch(u);
			return (res.headers.get("x-big") || "").length + " " + await res.text();
		} catch (e) { return e.name }
	}`
	g.Eq(g.page.MustEval(fetchText, "/big").Str(), "TypeError")
	g.Is(<-errs, rod.ErrResponseHeadersTooLarge)
	g.Eq(g.page.MustEval(fetchText, "/big?raised").Str(), strconv.Itoa(len(big))+" body")
	select {
	case err := <-errs:
		g.Fatal(err)
	default:
	}
}

func TestHijackContinueUnparsableURLsBrowser(t *testing.T) {
	g := setup(t)

	// net/http rejects invalid escapes in request targets, so a raw server
	// replies with the request target it receives.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	g.E(err)
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				requestLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				// Read the headers so that closing the connection does not reset it.
				for line := requestLine; line != "\r\n"; {
					if line, err = reader.ReadString('\n'); err != nil {
						return
					}
				}
				target := strings.Fields(requestLine)[1]
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: %d\r\n"+
					"Connection: close\r\n\r\n%s", len(target), target)
			}()
		}
	}()
	origin := "http://" + listener.Addr().String()

	router := g.page.HijackRequests()
	defer router.MustStop()

	errs := make(chan error, 8)
	router.OnError(func(err error) { errs <- err })
	router.MustAdd(origin+"/sale*", func(ctx *rod.Hijack) {
		ctx.Response.SetBody("handled")
	})

	go router.Run()

	g.page.MustNavigate(origin + "/").MustWaitLoad()
	fetchText := `async (u) => { try { return await (await fetch(u)).text() } catch (e) { return e.name } }`
	// Chrome keeps the invalid escape, which net/url rejects.
	g.Eq(g.page.MustEval(fetchText, "/sale-50%-off").Str(), "TypeError")
	_, ok := errors.AsType[*url.Error](<-errs)
	g.True(ok)

	router.ContinueUnparsableURLs(true)
	g.Eq(g.page.MustEval(fetchText, "/sale-50%-off").Str(), "/sale-50%-off")
	_, ok = errors.AsType[*url.Error](<-errs)
	g.True(ok)
	g.Eq(g.page.MustEval(fetchText, "/sale-50").Str(), "handled")
	select {
	case err := <-errs:
		g.Fatal(err)
	default:
	}
}

func TestHijackLoadResponseBodyLimitBrowser(t *testing.T) {
	g := setup(t)

	s := g.Serve().Route("/", ".html", `<body>ok</body>`)
	chunk := make([]byte, 32<<10)
	s.Mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})

	router := g.page.HijackRequests()
	defer router.MustStop()

	errs := make(chan error, 8)
	router.OnError(func(err error) { errs <- err })
	router.MustAdd(s.URL("/stream"), func(ctx *rod.Hijack) {
		ctx.MaxResponseBodyBytes = 1 << 20
		ctx.MustLoadResponse()
	})

	go router.Run()

	g.page.MustNavigate(s.URL()).MustWaitLoad()
	g.Eq(g.page.MustEval(`async () => {
		try { await (await fetch("/stream")).arrayBuffer(); return "loaded" } catch (e) { return e.name }
	}`).Str(), "TypeError")
	g.Is(<-errs, rod.ErrResponseBodyTooLarge)
}

func TestHijackLoadResponseHeadBrowser(t *testing.T) {
	g := setup(t)

	s := g.Serve().Route("/", ".html", `<body>ok</body>`)
	s.Mux.HandleFunc("/video", func(w http.ResponseWriter, _ *http.Request) {
		// The declared resource exceeds the body limit, but HEAD has no body.
		w.Header().Set("Content-Length", strconv.FormatInt(rod.DefaultMaxResponseBodyBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	})

	router := g.page.HijackRequests()
	defer router.MustStop()

	errs := make(chan error, 8)
	router.OnError(func(err error) { errs <- err })
	router.MustAdd(s.URL("/video"), func(ctx *rod.Hijack) {
		ctx.MustLoadResponse()
	})

	go router.Run()

	g.page.MustNavigate(s.URL()).MustWaitLoad()
	g.Eq(g.page.MustEval(`async () => {
		try {
			const res = await fetch("/video", {method: "HEAD"});
			return res.status + " " + res.headers.get("content-length");
		} catch (e) { return e.name }
	}`).Str(), "200 "+strconv.FormatInt(rod.DefaultMaxResponseBodyBytes+1, 10))
	select {
	case err := <-errs:
		g.Fatal(err)
	default:
	}
}

func TestHijackBinaryRedirectBrowser(t *testing.T) {
	g := setup(t)
	server := g.Serve().Route("/", ".html", "<body>ready</body>")
	received := make(chan []byte, 4)
	server.Mux.HandleFunc("/a+b/start", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		received <- body
		status := http.StatusTemporaryRedirect
		if r.URL.Query().Get("status") == "308" {
			status = http.StatusPermanentRedirect
		}
		http.Redirect(w, r, "/finish", status)
	})
	server.Mux.HandleFunc("/finish", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		received <- body
		w.Header().Add("Set-Cookie", "first=one; Path=/")
		w.Header().Add("Set-Cookie", "second=two; Path=/")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})
	g.page.MustNavigate(server.URL()).MustWaitLoad()
	router := g.page.HijackRequests()
	handled := make(chan []byte, 2)
	handlerErrors := make(chan error, 2)
	// Literal '+' and adjacent wildcards must agree with the browser's glob.
	g.E(router.Add(server.URL("/a+b/**"), "", func(h *rod.Hijack) {
		h.OnError = func(err error) { handlerErrors <- err }
		handled <- []byte(h.Request.Body())
		if err := h.LoadResponse(http.DefaultClient, true); err != nil {
			handlerErrors <- err
			h.Response.Fail(proto.NetworkErrorReasonFailed)
		}
	}))
	done := make(chan error, 1)
	go func() { done <- router.Run() }()
	defer func() {
		if err := router.Stop(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	want := []int{255, 0, 97, 254}
	for _, status := range []int{307, 308} {
		var got []int
		g.E(g.page.EvalJSON(&got, `async (url, bytes) => {
			const response = await fetch(url, {method: 'POST', body: new Uint8Array(bytes)});
			return Array.from(new Uint8Array(await response.arrayBuffer()));
		}`, server.URL(fmt.Sprintf("/a+b/start?status=%d", status)), want))
		if !slices.Equal(got, want) {
			t.Fatalf("browser received %v", got)
		}
		select {
		case body := <-handled:
			if !slices.Equal(body, []byte{255, 0, 97, 254}) {
				t.Fatalf("captured body = %x", body)
			}
		default:
			t.Fatal("literal glob did not dispatch the browser request")
		}
		for range 2 {
			select {
			case body := <-received:
				if !slices.Equal(body, []byte{255, 0, 97, 254}) {
					t.Fatalf("forwarded body = %x", body)
				}
			default:
				t.Fatal("redirect request was not replayed")
			}
		}
	}
	cookies := g.page.MustEval(`() => document.cookie`).Str()
	g.Has(cookies, "first=one")
	g.Has(cookies, "second=two")
	select {
	case err := <-handlerErrors:
		t.Fatal(err)
	default:
	}
}

// hijackRecorder is a Fetch client that records how paused requests resolve.
type hijackRecorder struct {
	mu      sync.Mutex
	actions []string
	fail    func(method string) error
}

func (r *hijackRecorder) call(_ context.Context, method string, params any) ([]byte, error) {
	if !strings.HasPrefix(method, "Fetch.") || method == "Fetch.enable" || method == "Fetch.disable" {
		return []byte(`{}`), nil
	}
	action := method
	if fail, ok := params.(proto.FetchFailRequest); ok {
		action += ":" + string(fail.ErrorReason)
	}
	r.mu.Lock()
	r.actions = append(r.actions, action)
	r.mu.Unlock()
	if r.fail != nil {
		if err := r.fail(method); err != nil {
			return nil, err
		}
	}
	return []byte(`{}`), nil
}

func (r *hijackRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	actions := r.actions
	r.actions = nil
	return actions
}

// publishPaused delivers a paused request and waits until the browser handles it.
func publishPaused(t *testing.T, events chan<- *cdp.Event, event proto.FetchRequestPaused) {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	sendEvent(events, "Fetch.requestPaused", "", string(data))
	synctest.Wait()
}

func TestHijackHandlerPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		recorder := &hijackRecorder{}
		events := make(chan *cdp.Event)
		browser := rod.New().Context(ctx).Client(&callTestClient{events: events, call: recorder.call})
		connectTestBrowser(t, browser)
		router := browser.HijackRequests()
		var mu sync.Mutex
		var reported, own []error
		router.OnError(func(err error) { mu.Lock(); reported = append(reported, err); mu.Unlock() })
		take := func() (routerErrs, ownErrs []error) {
			mu.Lock()
			defer mu.Unlock()
			routerErrs, ownErrs, reported, own = reported, own, nil, nil
			return
		}
		sentinel := errors.New("handler failure")
		mode := ""
		if err := router.Add("*", "", func(h *rod.Hijack) {
			switch mode {
			case "panic":
				panic(sentinel)
			case "own":
				h.OnError = func(err error) { mu.Lock(); own = append(own, err); mu.Unlock() }
				panic("custom handler")
			case "goexit":
				runtime.Goexit()
			case "nil":
				h.OnError = nil
				panic(sentinel)
			}
			h.Response.SetBody("ok")
		}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- router.Run() }()
		request := proto.FetchRequestPaused{RequestID: "one", Request: &proto.NetworkRequest{URL: "http://example.test/", Method: http.MethodGet, Headers: proto.NetworkHeaders{}}}
		failed := []string{"Fetch.failRequest:Failed"}

		for _, mode = range []string{"panic", "nil"} {
			publishPaused(t, events, request)
			routerErrs, _ := take()
			if actions := recorder.take(); !slices.Equal(actions, failed) || len(routerErrs) != 1 ||
				!errors.Is(routerErrs[0], &rod.TryError{}) || !errors.Is(routerErrs[0], sentinel) {
				t.Fatalf("%s: actions %v, errors %v", mode, actions, routerErrs)
			}
		}

		mode = "own"
		publishPaused(t, events, request)
		if routerErrs, ownErrs := take(); !slices.Equal(recorder.take(), failed) || len(routerErrs) != 0 ||
			len(ownErrs) != 1 || !errors.Is(ownErrs[0], &rod.TryError{}) {
			t.Fatalf("own handler errors: %v, %v", routerErrs, ownErrs)
		}

		mode = "goexit"
		publishPaused(t, events, request)
		if routerErrs, _ := take(); !slices.Equal(recorder.take(), failed) || len(routerErrs) != 1 ||
			!errors.Is(routerErrs[0], rod.ErrHijackHandlerExited) {
			t.Fatalf("goexit errors: %v", routerErrs)
		}

		// Failing to resolve the request is reported with the handler error.
		failErr := errors.New("Fetch.failRequest rejected")
		recorder.fail = func(method string) error {
			if method == "Fetch.failRequest" {
				return failErr
			}
			return nil
		}
		for _, mode = range []string{"panic", "goexit"} {
			publishPaused(t, events, request)
			if routerErrs, _ := take(); !slices.Equal(recorder.take(), failed) || len(routerErrs) != 1 || !errors.Is(routerErrs[0], failErr) {
				t.Fatalf("%s: resolve errors: %v", mode, routerErrs)
			}
		}

		// A panic while resolving does not resolve the request twice.
		recorder.fail = func(method string) error {
			if method == "Fetch.fulfillRequest" {
				panic(sentinel)
			}
			return nil
		}
		mode = ""
		publishPaused(t, events, request)
		if routerErrs, _ := take(); !slices.Equal(recorder.take(), []string{"Fetch.fulfillRequest"}) || len(routerErrs) != 1 ||
			!errors.Is(routerErrs[0], sentinel) {
			t.Fatalf("resolution panic: %v", routerErrs)
		}

		// The router keeps serving requests.
		recorder.fail = nil
		publishPaused(t, events, request)
		if routerErrs, _ := take(); !slices.Equal(recorder.take(), []string{"Fetch.fulfillRequest"}) || len(routerErrs) != 0 {
			t.Fatalf("later request: %v", routerErrs)
		}
		if err := router.Stop(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestHijackUnparsableURL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		recorder := &hijackRecorder{}
		events := make(chan *cdp.Event)
		browser := rod.New().Context(ctx).Client(&callTestClient{events: events, call: recorder.call})
		connectTestBrowser(t, browser)
		router := browser.HijackRequests()
		var mu sync.Mutex
		var reported []error
		router.OnError(func(err error) { mu.Lock(); reported = append(reported, err); mu.Unlock() })
		var handled atomic.Int32
		if err := router.Add("*", "", func(h *rod.Hijack) {
			handled.Add(1)
			if h.Request.URL() == nil {
				t.Error("handler received a nil URL")
			}
		}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- router.Run() }()

		publishPaused(t, events, proto.FetchRequestPaused{RequestID: "bad", Request: &proto.NetworkRequest{URL: "http://example.test/x%zz", Method: http.MethodGet, Headers: proto.NetworkHeaders{}}})
		mu.Lock()
		errs := reported
		mu.Unlock()
		if actions := recorder.take(); handled.Load() != 0 || !slices.Equal(actions, []string{"Fetch.failRequest:Failed"}) || len(errs) != 1 {
			t.Fatalf("handled %d, actions %v, errors %v", handled.Load(), actions, errs)
		}
		if _, ok := errors.AsType[*url.Error](errs[0]); !ok {
			t.Fatalf("URL error = %v", errs[0])
		}

		// Query strings are not validated by net/url and still reach handlers.
		publishPaused(t, events, proto.FetchRequestPaused{RequestID: "query", Request: &proto.NetworkRequest{URL: "http://example.test/?q=100%", Method: http.MethodGet, Headers: proto.NetworkHeaders{}}})
		if actions := recorder.take(); handled.Load() != 1 || !slices.Equal(actions, []string{"Fetch.fulfillRequest"}) {
			t.Fatalf("handled %d, actions %v", handled.Load(), actions)
		}
		if err := router.Stop(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestHijackContinueUnparsableURLs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		recorder := &hijackRecorder{}
		var mu sync.Mutex
		var continued []proto.FetchContinueRequest
		var reported []error
		events := make(chan *cdp.Event)
		browser := rod.New().Context(ctx).Client(&callTestClient{events: events, call: func(ctx context.Context, method string, params any) ([]byte, error) {
			if params, ok := params.(proto.FetchContinueRequest); ok {
				mu.Lock()
				continued = append(continued, params)
				mu.Unlock()
			}
			return recorder.call(ctx, method, params)
		}})
		connectTestBrowser(t, browser)
		router := browser.HijackRequests()
		if router.ContinueUnparsableURLs(true) != router {
			t.Fatal("ContinueUnparsableURLs does not return the router")
		}
		router.OnError(func(err error) { mu.Lock(); reported = append(reported, err); mu.Unlock() })
		take := func() (actions []string, errs []error) {
			mu.Lock()
			defer mu.Unlock()
			errs, reported = reported, nil
			return recorder.take(), errs
		}
		var handled atomic.Int32
		if err := router.Add("*", "", func(*rod.Hijack) { handled.Add(1) }); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- router.Run() }()
		request := proto.FetchRequestPaused{
			RequestID: "bad", FrameID: "frame", ResourceType: proto.NetworkResourceTypeFetch,
			Request: &proto.NetworkRequest{
				URL: "http://example.test/sale-50%-off", Method: http.MethodGet, Headers: proto.NetworkHeaders{},
				InitialPriority: proto.NetworkResourcePriorityHigh, ReferrerPolicy: proto.NetworkRequestReferrerPolicyNoReferrer,
			},
		}

		// The request continues unmodified, and the parse error is still reported.
		publishPaused(t, events, request)
		actions, errs := take()
		if handled.Load() != 0 || !slices.Equal(actions, []string{"Fetch.continueRequest"}) || len(errs) != 1 {
			t.Fatalf("handled %d, actions %v, errors %v", handled.Load(), actions, errs)
		}
		if _, ok := errors.AsType[*url.Error](errs[0]); !ok {
			t.Fatalf("URL error = %v", errs[0])
		}
		mu.Lock()
		if !reflect.DeepEqual(continued, []proto.FetchContinueRequest{{RequestID: "bad"}}) {
			t.Fatalf("continued = %+v", continued)
		}
		mu.Unlock()

		// A failure to continue is reported with the parse error.
		continueErr := errors.New("Fetch.continueRequest rejected")
		recorder.fail = func(method string) error {
			if method == "Fetch.continueRequest" {
				return continueErr
			}
			return nil
		}
		publishPaused(t, events, request)
		actions, errs = take()
		if !slices.Equal(actions, []string{"Fetch.continueRequest"}) || len(errs) != 1 || !errors.Is(errs[0], continueErr) {
			t.Fatalf("actions %v, errors %v", actions, errs)
		}
		if _, ok := errors.AsType[*url.Error](errs[0]); !ok {
			t.Fatalf("URL error = %v", errs[0])
		}
		recorder.fail = nil

		// Disabling it fails such requests again.
		router.ContinueUnparsableURLs(false)
		publishPaused(t, events, request)
		if actions, errs := take(); handled.Load() != 0 || !slices.Equal(actions, []string{"Fetch.failRequest:Failed"}) || len(errs) != 1 {
			t.Fatalf("actions %v, errors %v", actions, errs)
		}
		if err := router.Stop(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

// A paused request without its required request object is malformed protocol
// data: the router stops with the decoding error instead of running handlers.
func TestHijackMissingRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		recorder := &hijackRecorder{}
		events := make(chan *cdp.Event)
		browser := rod.New().Context(ctx).Client(&callTestClient{events: events, call: recorder.call})
		connectTestBrowser(t, browser)
		router := browser.HijackRequests()
		if err := router.Add("*", "", func(*rod.Hijack) { t.Error("handler ran for a malformed event") }); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- router.Run() }()

		publishPaused(t, events, proto.FetchRequestPaused{RequestID: "missing"})
		if err := <-done; !errors.Is(err, proto.ErrMissingField) {
			t.Fatalf("Run = %v", err)
		}
		if actions := recorder.take(); len(actions) != 0 {
			t.Fatalf("actions = %v", actions)
		}
	})
}

func TestHijackRouteDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var actions []string
		client := &callTestClient{events: make(chan *cdp.Event), call: func(_ context.Context, method string, _ any) ([]byte, error) {
			if method == "Fetch.fulfillRequest" || method == "Fetch.continueRequest" {
				actions = append(actions, method)
			}
			return []byte(`{}`), nil
		}}
		browser := rod.New().Context(ctx).Client(client)
		connectTestBrowser(t, browser)
		router := browser.HijackRequests()
		seen := []string{}
		if err := router.Add("*", proto.NetworkResourceTypeImage, func(*rod.Hijack) { seen = append(seen, "wrong") }); err != nil {
			t.Fatal(err)
		}
		if err := router.Add("*", proto.NetworkResourceTypeDocument, func(h *rod.Hijack) { seen = append(seen, "skip"); h.Skip = true }); err != nil {
			t.Fatal(err)
		}
		if err := router.Add("*", proto.NetworkResourceTypeDocument, func(h *rod.Hijack) { seen = append(seen, "fulfill"); h.Response.SetBody("ok") }); err != nil {
			t.Fatal(err)
		}
		if err := router.Add("*", "", func(*rod.Hijack) { seen = append(seen, "late") }); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- router.Run() }()
		publish := func(kind proto.NetworkResourceType) {
			data, err := json.Marshal(proto.FetchRequestPaused{RequestID: "one", ResourceType: kind, Request: &proto.NetworkRequest{URL: "http://example.test/", Method: http.MethodGet, Headers: proto.NetworkHeaders{}}})
			if err != nil {
				t.Fatal(err)
			}
			sendEvent(client.events, "Fetch.requestPaused", "", string(data))
			synctest.Wait()
		}
		publish(proto.NetworkResourceTypeDocument)
		if !slices.Equal(seen, []string{"skip", "fulfill"}) || !slices.Equal(actions, []string{"Fetch.fulfillRequest"}) {
			t.Fatalf("dispatch = %v, %v", seen, actions)
		}
		if err := router.Remove("*"); err != nil {
			t.Fatal(err)
		}
		publish(proto.NetworkResourceTypeDocument)
		if !slices.Equal(actions, []string{"Fetch.fulfillRequest", "Fetch.continueRequest"}) {
			t.Fatal(actions)
		}
		if err := router.Stop(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := router.Add("after-stop", "", func(*rod.Hijack) {}); err == nil {
			t.Fatal("stopped router enabled Fetch again")
		}
	})
}

func TestHijackLifecycleErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupErr, cleanupErr := errors.New("setup failed"), errors.New("cleanup failed")
		browser := rod.New().Context(t.Context()).Client(&callTestClient{call: func(ctx context.Context, method string, _ any) ([]byte, error) {
			if method == "Fetch.enable" {
				return nil, setupErr
			}
			if ctx.Err() != nil {
				t.Error("cleanup inherited cancellation")
			}
			return nil, cleanupErr
		}})
		router := browser.HijackRequests()
		if err := router.Run(); !errors.Is(err, setupErr) || !errors.Is(err, cleanupErr) {
			t.Fatal(err)
		}
		if err := router.Add("*", "", func(*rod.Hijack) {}); !errors.Is(err, setupErr) {
			t.Fatal(err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		browser := rod.New().Context(t.Context()).Client(&callTestClient{call: func(ctx context.Context, method string, _ any) ([]byte, error) {
			if method == "Fetch.disable" && ctx.Err() != nil {
				t.Error("cleanup inherited cancellation")
			}
			return []byte(`{}`), nil
		}})
		connectTestBrowser(t, browser)
		router := browser.Context(ctx).HijackRequests()
		cancel()
		if err := router.Run(); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestHandleAuthScope(t *testing.T) {
	type answer struct {
		requestID proto.FetchRequestID
		response  proto.FetchAuthChallengeResponse
	}
	credentials := rod.AuthCredentials{
		Source: proto.FetchAuthChallengeSourceProxy, Origin: "HTTP://Proxy.Example.test", Username: "user", Password: "secret",
	}
	for _, test := range []struct {
		name      string
		challenge *proto.FetchAuthChallenge
		provide   bool
	}{
		{"server challenge from proxy origin", &proto.FetchAuthChallenge{Source: proto.FetchAuthChallengeSourceServer, Origin: "http://proxy.example.test"}, false},
		{"proxy on another port", &proto.FetchAuthChallenge{Source: proto.FetchAuthChallengeSourceProxy, Origin: "http://proxy.example.test:3128"}, false},
		{"proxy with another scheme", &proto.FetchAuthChallenge{Source: proto.FetchAuthChallengeSourceProxy, Origin: "https://proxy.example.test"}, false},
		{"proxy on another host", &proto.FetchAuthChallenge{Source: proto.FetchAuthChallengeSourceProxy, Origin: "http://attacker.example.test"}, false},
		{"missing source", &proto.FetchAuthChallenge{Origin: "http://proxy.example.test"}, false},
		{"explicit default port", &proto.FetchAuthChallenge{Source: proto.FetchAuthChallengeSourceProxy, Origin: "http://proxy.example.test:80"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				answers := make(chan answer, 2)
				events := make(chan *cdp.Event)
				browser := rod.New().Context(ctx).Client(&callTestClient{events: events, call: func(_ context.Context, method string, params any) ([]byte, error) {
					if method == "Fetch.continueWithAuth" {
						auth := params.(proto.FetchContinueWithAuth)
						answers <- answer{auth.RequestID, *auth.AuthChallengeResponse}
					}
					return []byte(`{}`), nil
				}})
				connectTestBrowser(t, browser)
				done := make(chan error, 1)
				wait := browser.HandleAuth(credentials)
				go func() { done <- wait() }()
				event, err := json.Marshal(proto.FetchAuthRequired{
					RequestID: "challenge", Request: &proto.NetworkRequest{URL: "http://example.test/", Headers: proto.NetworkHeaders{}}, AuthChallenge: test.challenge,
				})
				if err != nil {
					t.Fatal(err)
				}
				sendEvent(events, "Fetch.authRequired", "", string(event))
				synctest.Wait()
				got := <-answers
				if !test.provide {
					if got.requestID != "challenge" || got.response != (proto.FetchAuthChallengeResponse{Response: proto.FetchAuthChallengeResponseResponseDefault}) {
						t.Fatalf("non-matching answer = %+v", got)
					}
					select {
					case err := <-done:
						t.Fatalf("wait ended after a non-matching challenge: %v", err)
					default:
					}
					// The wait continues until the matching challenge.
					sendEvent(events, "Fetch.authRequired", "",
						`{"requestId":"proxy","request":{"url":"http://example.test/","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"authChallenge":{"source":"Proxy","origin":"http://proxy.example.test","scheme":"","realm":""},"frameId":"","resourceType":""}`)
					got = <-answers
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				want := proto.FetchAuthChallengeResponse{
					Response: proto.FetchAuthChallengeResponseResponseProvideCredentials, Username: "user", Password: "secret",
				}
				if got.response != want {
					t.Fatalf("matching answer = %+v", got)
				}
			})
		})
	}
}

// With AnyOrigin, the first challenge from the credentials' source receives
// them whatever its origin, and challenges from the other source still receive
// the default response.
func TestHandleAuthAnyOrigin(t *testing.T) {
	provide := proto.FetchAuthChallengeResponse{
		Response: proto.FetchAuthChallengeResponseResponseProvideCredentials, Username: "user", Password: "secret",
	}
	fallback := proto.FetchAuthChallengeResponse{Response: proto.FetchAuthChallengeResponseResponseDefault}
	for source, other := range map[proto.FetchAuthChallengeSource]proto.FetchAuthChallengeSource{
		proto.FetchAuthChallengeSourceProxy:  proto.FetchAuthChallengeSourceServer,
		proto.FetchAuthChallengeSourceServer: proto.FetchAuthChallengeSourceProxy,
	} {
		t.Run(string(source), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				answers := make(chan proto.FetchContinueWithAuth, 2)
				events := make(chan *cdp.Event)
				browser := rod.New().Context(ctx).Client(&callTestClient{events: events, call: func(_ context.Context, method string, params any) ([]byte, error) {
					if method == "Fetch.continueWithAuth" {
						answers <- params.(proto.FetchContinueWithAuth)
					}
					return []byte(`{}`), nil
				}})
				connectTestBrowser(t, browser)
				done := make(chan error, 1)
				wait := browser.HandleAuth(rod.AuthCredentials{Source: source, AnyOrigin: true, Username: "user", Password: "secret"})
				go func() { done <- wait() }()
				challenge := func(id proto.FetchRequestID, source proto.FetchAuthChallengeSource, origin string) proto.FetchContinueWithAuth {
					event, err := json.Marshal(proto.FetchAuthRequired{
						RequestID: id, FrameID: "frame", ResourceType: proto.NetworkResourceTypeDocument,
						Request: &proto.NetworkRequest{
							URL: "http://example.test/", Method: http.MethodGet, Headers: proto.NetworkHeaders{},
							InitialPriority: proto.NetworkResourcePriorityVeryHigh, ReferrerPolicy: proto.NetworkRequestReferrerPolicyNoReferrer,
						},
						AuthChallenge: &proto.FetchAuthChallenge{Source: source, Origin: origin, Scheme: "basic", Realm: "realm"},
					})
					if err != nil {
						t.Fatal(err)
					}
					sendEvent(events, "Fetch.authRequired", "", string(event))
					synctest.Wait()
					return <-answers
				}

				if got := challenge("other", other, "http://example.test"); got.RequestID != "other" || *got.AuthChallengeResponse != fallback {
					t.Fatalf("other source answer = %+v", got)
				}
				select {
				case err := <-done:
					t.Fatalf("wait ended after a challenge from another source: %v", err)
				default:
				}
				if got := challenge("any", source, "https://unlisted.example.test:8443"); got.RequestID != "any" || *got.AuthChallengeResponse != provide {
					t.Fatalf("matching answer = %+v", got)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

// A Fetch event without its required objects ends the wait with the decoding
// error. The event is not answered, so a challenge never receives the
// credentials.
func TestHandleAuthMissingObjects(t *testing.T) {
	for _, event := range []struct{ method, data string }{
		{"Fetch.authRequired", `{"requestId":"challenge","request":{"url":"http://example.test/","method":"GET","headers":{},"initialPriority":"High","referrerPolicy":"no-referrer"},"frameId":"frame","resourceType":"Document"}`},
		{"Fetch.requestPaused", `{"requestId":"paused","frameId":"frame","resourceType":"Document"}`},
	} {
		t.Run(event.method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var answered atomic.Bool
				events := make(chan *cdp.Event)
				browser := rod.New().Context(ctx).Client(&callTestClient{events: events, call: func(_ context.Context, method string, _ any) ([]byte, error) {
					if method == "Fetch.continueWithAuth" || method == "Fetch.continueRequest" {
						answered.Store(true)
					}
					return []byte(`{}`), nil
				}})
				connectTestBrowser(t, browser)
				wait := browser.HandleAuth(rod.AuthCredentials{
					Source: proto.FetchAuthChallengeSourceProxy, Origin: "http://proxy.example.test", Username: "user", Password: "secret",
				})
				sendEvent(events, event.method, "", event.data)
				if err := wait(); !errors.Is(err, proto.ErrMissingField) {
					t.Fatalf("wait = %v", err)
				}
				if answered.Load() {
					t.Fatal("a malformed event was answered")
				}
			})
		})
	}
}
