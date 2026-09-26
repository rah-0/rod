package rod

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod/internal/observable"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

type hijackTestClient struct {
	call func(context.Context, string, any) ([]byte, error)
}

func (c *hijackTestClient) Call(ctx context.Context, _ string, method string, args any) ([]byte, error) {
	return c.call(ctx, method, args)
}

func (*hijackTestClient) Event() <-chan *cdp.Event { return nil }

func newTestHijack(t *testing.T, event *proto.FetchRequestPaused) *Hijack {
	t.Helper()
	h, err := newHijackRouter(New(), nil).new(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHijackRepeatedHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "session=one")
		w.Header().Add("Set-Cookie", "csrf=two")
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	h := newTestHijack(t, &proto.FetchRequestPaused{
		Request: &proto.NetworkRequest{URL: server.URL, Method: http.MethodGet},
	})
	for range 2 {
		if err := h.LoadResponse(server.Client(), true); err != nil {
			t.Fatal(err)
		}
		if got := h.Response.Headers().Values("Set-Cookie"); !slices.Equal(got, []string{"session=one", "csrf=two"}) {
			t.Fatalf("repeated response headers = %v", got)
		}
	}
	h.Response.SetHeader("sEt-CoOkIe", "only=three")
	if got := h.Response.Headers().Values("Set-Cookie"); !slices.Equal(got, []string{"only=three"}) {
		t.Fatalf("replacement retained earlier values: %v", got)
	}
	h.Response.AddHeader("Set-Cookie", "extra=four")
	if got := h.Response.Headers().Values("Set-Cookie"); len(got) != 2 {
		t.Fatal(got)
	}
}

func TestHijackLoadResponseReplacesHeaders(t *testing.T) {
	h := newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: "http://example.test", Method: http.MethodGet}})
	h.Response.AddHeader("x-mocked", "old", "Kept", "yes", "X-MOCKED", "older", "Empty", "stays")
	h.Response.replaceHeaders(http.Header{"X-Mocked": {"new", "newer"}, "Empty": {}})
	headers := h.Response.Headers()
	if got := headers.Values("X-Mocked"); !slices.Equal(got, []string{"new", "newer"}) {
		t.Fatalf("replaced values = %v", got)
	}
	if headers.Get("Kept") != "yes" || headers.Get("Empty") != "stays" {
		t.Fatalf("unrelated headers changed: %v", headers)
	}
}

func TestHijackLoadResponseHeaderLimit(t *testing.T) {
	const many = 20000
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			// JSON escapes each '<' to six bytes in the fulfill message.
			w.Header().Set("X-Big", strings.Repeat("<", int(DefaultMaxResponseHeaderBytes)))
		case "/many":
			for i := range many {
				w.Header().Set("H"+strconv.Itoa(i), "v")
			}
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	load := func(path string, limit int64) (*Hijack, error) {
		h := newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: server.URL + path, Method: http.MethodGet}})
		if h.MaxResponseHeaderBytes != DefaultMaxResponseHeaderBytes {
			t.Fatalf("initial limit = %d", h.MaxResponseHeaderBytes)
		}
		h.MaxResponseHeaderBytes = limit
		return h, h.LoadResponse(server.Client(), true)
	}

	// Zero and negative limits select the default.
	for _, limit := range []int64{DefaultMaxResponseHeaderBytes, 0, -1} {
		h, err := load("/big", limit)
		if !errors.Is(err, ErrResponseHeadersTooLarge) {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if len(h.Response.Payload().ResponseHeaders) != 0 || h.Response.Body() != "" || h.Response.RawResponse != nil {
			t.Fatalf("limit %d: partial response applied: %+v", limit, h.Response.Payload())
		}
		// Many distinct names within the limit are applied in one pass.
		h, err = load("/many", limit)
		if err != nil || h.Response.Body() != "ok" || h.Response.Headers().Get("H"+strconv.Itoa(many-1)) != "v" {
			t.Fatalf("limit %d: headers within the limit: %v", limit, err)
		}
	}

	// A raised limit applies larger headers.
	h, err := load("/big", 2*DefaultMaxResponseHeaderBytes)
	if err != nil || len(h.Response.Headers().Get("X-Big")) != int(DefaultMaxResponseHeaderBytes) || h.Response.Body() != "ok" {
		t.Fatalf("raised limit: %v", err)
	}

	// A lowered limit rejects headers above it and accepts headers at it.
	size := responseHeaderSize(h.Response.RawResponse.Header)
	h, err = load("/big", size-1)
	if !errors.Is(err, ErrResponseHeadersTooLarge) || h.Response.RawResponse != nil {
		t.Fatalf("lowered limit: %v", err)
	}
	if _, err := load("/big", size); err != nil {
		t.Fatalf("exact limit: %v", err)
	}

	// MustLoadResponse uses the same limit.
	h = newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: server.URL + "/many", Method: http.MethodGet}})
	h.MaxResponseHeaderBytes = 1024
	if err := Try(h.MustLoadResponse); !errors.Is(err, ErrResponseHeadersTooLarge) || h.Response.RawResponse != nil {
		t.Fatalf("MustLoadResponse: %v", err)
	}
}

func TestHijackReplayableBodies(t *testing.T) {
	binary := []byte{0xff, 0, 'a', 0xfe}
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, representation := range []string{"entries", "legacy", "replacement", "JSON", "empty"} {
			t.Run(fmt.Sprintf("%d/%s", status, representation), func(t *testing.T) {
				observed := make(chan []byte, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					if r.Method != http.MethodPost {
						t.Errorf("redirect changed method: %s", r.Method)
					}
					observed <- body
					if r.URL.Path == "/start" {
						http.Redirect(w, r, "/finish", status)
						return
					}
					_, _ = w.Write(body)
				}))
				defer server.Close()
				event := &proto.FetchRequestPaused{Request: &proto.NetworkRequest{
					URL: server.URL + "/start", Method: http.MethodPost, HasPostData: true,
					PostData: "legacy",
				}}
				if representation == "entries" {
					event.Request.PostDataEntries = []*proto.NetworkPostDataEntry{{Bytes: binary[:2]}, nil, {Bytes: binary[2:]}}
				}
				h := newTestHijack(t, event)
				want := []byte("legacy")
				switch representation {
				case "entries":
					want = binary
				case "replacement":
					want = binary
					input := bytes.Clone(binary)
					h.Request.SetBody(input)
					input[0] = 0 // replay must not alias the caller's mutable slice
				case "JSON":
					want = []byte(`{"ok":true}`)
					h.Request.SetBody(map[string]bool{"ok": true})
				case "empty":
					want = nil
					h.Request.SetBody("")
				}
				if !bytes.Equal([]byte(h.Request.Body()), want) || h.Request.Req().ContentLength != int64(len(want)) {
					t.Fatalf("body metadata: %q, %d", h.Request.Body(), h.Request.Req().ContentLength)
				}
				for range 2 {
					replay, err := h.Request.Req().GetBody()
					if err != nil {
						t.Fatal(err)
					}
					got, err := io.ReadAll(replay)
					_ = replay.Close()
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("replay = %x, %v", got, err)
					}
				}
				// The browser follows redirects, so LoadResponse returns the first hop.
				if err := h.LoadResponse(server.Client(), true); err != nil {
					t.Fatal(err)
				}
				if h.Response.Payload().ResponseCode != status || h.Response.Headers().Get("Location") != "/finish" {
					t.Fatalf("redirect = %d, %v", h.Response.Payload().ResponseCode, h.Response.Headers())
				}
				if len(observed) != 1 {
					t.Fatalf("server received %d requests", len(observed))
				}
				if first := <-observed; !bytes.Equal(first, want) {
					t.Fatalf("server body = %x", first)
				}
			})
		}
	}
	without := newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: "http://example.test", Method: http.MethodGet}})
	if without.Request.Req().Body != nil || without.Request.Req().GetBody != nil {
		t.Fatal("invented an absent body")
	}
	without.Request.SetBody("")
	if without.Request.Req().Body != http.NoBody {
		t.Fatal("empty replacement is not explicit")
	}
}

func TestHijackLoadResponseRedirect(t *testing.T) {
	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHits.Add(1)
		_, _ = io.WriteString(w, "internal secret")
	}))
	defer internal.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "hop=first")
		http.Redirect(w, r, internal.URL+"/secret", http.StatusFound)
	}))
	defer front.Close()

	followed := 0
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		followed++
		return nil
	}}
	for _, c := range []*http.Client{client, nil} {
		h := newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: front.URL + "/redir", Method: http.MethodGet}})
		if err := h.LoadResponse(c, true); err != nil {
			t.Fatal(err)
		}
		headers := h.Response.Headers()
		if h.Response.Payload().ResponseCode != http.StatusFound || headers.Get("Location") != internal.URL+"/secret" ||
			headers.Get("Set-Cookie") != "hop=first" || strings.Contains(h.Response.Body(), "internal secret") {
			t.Fatalf("response = %d, %v, %q", h.Response.Payload().ResponseCode, headers, h.Response.Body())
		}
	}
	if internalHits.Load() != 0 || followed != 0 {
		t.Fatalf("redirect followed: internal hits %d, CheckRedirect calls %d", internalHits.Load(), followed)
	}
	if client.CheckRedirect == nil {
		t.Fatal("LoadResponse modified the caller's client")
	}
}

func TestHijackLoadResponseFollowRedirects(t *testing.T) {
	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHits.Add(1)
		w.Header().Set("Set-Cookie", "session=internal")
		_, _ = io.WriteString(w, "internal secret")
	}))
	defer internal.Close()
	received := make(chan string, 4)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redir":
			w.Header().Set("Set-Cookie", "hop=front")
			http.Redirect(w, r, internal.URL+"/secret", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/post":
			body, _ := io.ReadAll(r.Body)
			received <- string(body)
			http.Redirect(w, r, "/echo", http.StatusTemporaryRedirect)
		case "/echo":
			body, _ := io.ReadAll(r.Body)
			received <- string(body)
			_, _ = w.Write(body)
		}
	}))
	defer front.Close()
	paused := func(method, path string) *Hijack {
		h := newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: front.URL + path, Method: method}})
		if h.FollowRedirects {
			t.Fatal("redirects are followed by default")
		}
		h.FollowRedirects = true
		return h
	}
	unchanged := func(h *Hijack) bool {
		return h.Response.Payload().ResponseCode == http.StatusOK && len(h.Response.Payload().ResponseHeaders) == 0 &&
			h.Response.Body() == "" && h.Response.RawResponse == nil
	}

	// Without a CheckRedirect, the client's default policy applies.
	for _, c := range []*http.Client{{}, nil} {
		h := paused(http.MethodGet, "/redir")
		if err := h.LoadResponse(c, true); err != nil {
			t.Fatal(err)
		}
		// The final response's headers apply to the original URL, and those of
		// the redirect response are dropped.
		if h.Response.Payload().ResponseCode != http.StatusOK || h.Response.Body() != "internal secret" ||
			h.Response.RawResponse.Request.URL.String() != internal.URL+"/secret" ||
			!slices.Equal(h.Response.Headers().Values("Set-Cookie"), []string{"session=internal"}) {
			t.Fatalf("followed = %d, %q, %v", h.Response.Payload().ResponseCode, h.Response.Body(), h.Response.Headers())
		}
	}
	if internalHits.Load() != 2 {
		t.Fatalf("internal hits = %d", internalHits.Load())
	}
	h := paused(http.MethodGet, "/loop")
	if err := h.LoadResponse(nil, true); err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") || !unchanged(h) {
		t.Fatalf("redirect loop: %v", err)
	}

	// The client's own policy decides.
	var via []string
	sameHost := &http.Client{CheckRedirect: func(req *http.Request, previous []*http.Request) error {
		via = append(via, req.URL.String())
		if req.URL.Host != previous[0].URL.Host {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	h = paused(http.MethodGet, "/redir")
	if err := h.LoadResponse(sameHost, true); err != nil {
		t.Fatal(err)
	}
	if h.Response.Payload().ResponseCode != http.StatusFound || h.Response.Headers().Get("Location") != internal.URL+"/secret" ||
		!slices.Equal(via, []string{internal.URL + "/secret"}) || internalHits.Load() != 2 {
		t.Fatalf("CheckRedirect = %d, %v, %v", h.Response.Payload().ResponseCode, h.Response.Headers(), via)
	}
	rejected := errors.New("redirect rejected")
	h = paused(http.MethodGet, "/redir")
	if err := h.LoadResponse(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return rejected }}, true); !errors.Is(err, rejected) || !unchanged(h) {
		t.Fatalf("CheckRedirect error: %v", err)
	}

	// A 307 replays the request body to the redirect target.
	h = paused(http.MethodPost, "/post")
	h.Request.SetBody("payload")
	if err := h.LoadResponse(nil, true); err != nil || h.Response.Body() != "payload" {
		t.Fatalf("replayed body = %q, %v", h.Response.Body(), err)
	}
	if first, second := <-received, <-received; first != "payload" || second != "payload" {
		t.Fatalf("server bodies = %q, %q", first, second)
	}

	// MustLoadResponse uses the same setting.
	h = paused(http.MethodGet, "/redir")
	h.MustLoadResponse()
	if h.Response.Body() != "internal secret" {
		t.Fatalf("MustLoadResponse followed = %q", h.Response.Body())
	}
	h = paused(http.MethodGet, "/redir")
	h.FollowRedirects = false
	h.MustLoadResponse()
	if h.Response.Payload().ResponseCode != http.StatusFound || internalHits.Load() != 3 {
		t.Fatalf("MustLoadResponse without following = %d", h.Response.Payload().ResponseCode)
	}
}

func TestHijackLoadResponseBodyLimit(t *testing.T) {
	chunk := bytes.Repeat([]byte("x"), 32<<10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream": // endless body without Content-Length
			w.Header().Set("X-Stream", "yes")
			for r.Context().Err() == nil {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		case "/declared":
			w.Header().Set("Content-Length", "2048")
			w.WriteHeader(http.StatusOK)
		default:
			_, _ = w.Write(chunk[:1024])
		}
	}))
	defer server.Close()
	// Over HTTP/2, Go reports the Content-Length of a 204 or 304 response but
	// fails to read the missing body.
	bodiless := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2048")
		code, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		w.WriteHeader(code)
	}))
	bodiless.EnableHTTP2 = true
	bodiless.StartTLS()
	defer bodiless.Close()
	loadFrom := func(s *httptest.Server, method, path string, limit int64, loadBody bool) (*Hijack, error) {
		h := newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: s.URL + path, Method: method}})
		if h.MaxResponseBodyBytes != DefaultMaxResponseBodyBytes {
			t.Fatalf("initial limit = %d", h.MaxResponseBodyBytes)
		}
		h.MaxResponseBodyBytes = limit
		return h, h.LoadResponse(s.Client(), loadBody)
	}
	load := func(path string, limit int64, loadBody bool) (*Hijack, error) {
		return loadFrom(server, http.MethodGet, path, limit, loadBody)
	}

	for _, path := range []string{"/stream", "/declared"} {
		h, err := load(path, 1024, true)
		if !errors.Is(err, ErrResponseBodyTooLarge) {
			t.Fatalf("%s: %v", path, err)
		}
		// A failed load leaves the default response untouched.
		if h.Response.Payload().ResponseCode != http.StatusOK || len(h.Response.Payload().ResponseHeaders) != 0 ||
			h.Response.Body() != "" || h.Response.RawResponse != nil {
			t.Fatalf("%s: partial response applied: %+v", path, h.Response.Payload())
		}
	}
	// The default limit also stops an endless body.
	if _, err := load("/stream", 0, true); !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatal(err)
	}
	h, err := load("/exact", 1024, true)
	if err != nil || len(h.Response.Body()) != 1024 {
		t.Fatalf("exact limit = %d, %v", len(h.Response.Body()), err)
	}
	h, err = load("/stream", 1024, false)
	if err != nil || h.Response.Headers().Get("X-Stream") != "yes" || h.Response.Body() != "" {
		t.Fatalf("headers only = %v, %v", h.Response.Headers(), err)
	}

	for _, c := range []struct {
		server *httptest.Server
		method string
		path   string
		code   int
	}{
		{server, http.MethodHead, "/declared", http.StatusOK},
		{bodiless, http.MethodHead, "/200", http.StatusOK},
		{bodiless, http.MethodGet, "/204", http.StatusNoContent},
		{bodiless, http.MethodGet, "/304", http.StatusNotModified},
	} {
		for _, limit := range []int64{1024, 0} {
			h, err := loadFrom(c.server, c.method, c.path, limit, true)
			// A nil body would reach Chrome as null instead of an empty body.
			if err != nil || h.Response.Payload().ResponseCode != c.code || h.Response.Payload().Body == nil ||
				len(h.Response.Payload().Body) != 0 || h.Response.Headers().Get("Content-Length") != "2048" {
				t.Fatalf("%s %s, limit %d: %v, %+v", c.method, c.path, limit, err, h.Response.Payload())
			}
		}
	}
}

func TestHijackRouteMutation(t *testing.T) {
	sentinel := errors.New("Fetch.enable rejected")
	reject := false
	var enabled proto.FetchEnable
	client := &hijackTestClient{call: func(_ context.Context, method string, params any) ([]byte, error) {
		if reject {
			return nil, sentinel
		}
		if method == "Fetch.enable" {
			enabled = params.(proto.FetchEnable)
		}
		return []byte(`{}`), nil
	}}
	router := newHijackRouter(New(), client)
	if err := router.Add("**.example.test/**", proto.NetworkResourceTypeImage, func(*Hijack) {}); err != nil {
		t.Fatal(err)
	}
	if err := router.Add("other", proto.NetworkResourceTypeDocument, func(*Hijack) {}); err != nil {
		t.Fatal(err)
	}
	before := slices.Clone(router.handlers)
	beforeEnable := enabled
	reject = true
	if err := router.Add("third", "", func(*Hijack) {}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := router.Remove("other"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if !slices.Equal(router.handlers, before) || !reflect.DeepEqual(*router.enable, beforeEnable) {
		t.Fatal("failed update mutated routes")
	}
	reject = false
	if err := router.Remove("other"); err != nil {
		t.Fatal(err)
	}
	if len(enabled.Patterns) != 1 || enabled.Patterns[0].ResourceType != proto.NetworkResourceTypeImage {
		t.Fatal("removal lost retained resource filter")
	}
	if err := router.Add("nil", "", nil); err == nil {
		t.Fatal("accepted a nil handler")
	}
}

func TestHandleAuthErrorsAndRestore(t *testing.T) {
	for _, phase := range []string{"setup", "wait", "continue", "auth", "restore"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				sentinel := errors.New("protocol failure")
				var restored proto.FetchEnable
				browser := New().Context(ctx).Client(&hijackTestClient{call: func(callCtx context.Context, method string, params any) ([]byte, error) {
					switch method {
					case "Fetch.enable":
						enable := params.(proto.FetchEnable)
						if enable.HandleAuthRequests != nil && *enable.HandleAuthRequests {
							if phase == "setup" {
								return nil, sentinel
							}
						} else {
							if callCtx.Err() != nil {
								t.Error("restore inherited cancellation")
							}
							restored = enable
							if phase == "restore" {
								return nil, sentinel
							}
						}
					case "Fetch.continueRequest":
						if phase == "continue" {
							return nil, sentinel
						}
					case "Fetch.continueWithAuth":
						if phase == "auth" {
							return nil, sentinel
						}
					}
					return []byte(`{}`), nil
				}})
				browser.event = observable.New[*Message](ctx)
				previous := proto.FetchEnable{Patterns: []*proto.FetchRequestPattern{{URLPattern: "original", ResourceType: proto.NetworkResourceTypeImage}}}
				browser.set("", "Fetch.enable", previous)
				wait := browser.HandleAuth(AuthCredentials{
					Source: proto.FetchAuthChallengeSourceServer, Origin: "http://example.test", Username: "user", Password: "password",
				})
				if phase == "wait" {
					cancel()
				} else if phase != "setup" {
					browser.event.Publish(&Message{Method: "Fetch.requestPaused", data: []byte(`{"requestId":"one","request":{"url":"http://example.test/","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"frameId":"","resourceType":""}`)})
					browser.event.Publish(&Message{Method: "Fetch.authRequired", data: []byte(`{"requestId":"one","request":{"url":"http://example.test/","method":"","headers":{},"initialPriority":"","referrerPolicy":""},"authChallenge":{"source":"Server","origin":"http://example.test","scheme":"","realm":""},"frameId":"","resourceType":""}`)})
				}
				want := sentinel
				if phase == "wait" {
					want = context.Canceled
				}
				if err := wait(); !errors.Is(err, want) {
					t.Fatalf("wait = %v, want %v", err, want)
				}
				if !reflect.DeepEqual(restored, previous) {
					t.Fatalf("restored = %+v", restored)
				}
			})
		})
	}
}

func TestHandleAuthValidation(t *testing.T) {
	for _, credentials := range []AuthCredentials{
		{Origin: "http://example.test"},
		{Source: "Other", Origin: "http://example.test"},
		{Source: proto.FetchAuthChallengeSourceServer},
		{Source: proto.FetchAuthChallengeSourceServer, Origin: "example.test"},
		{Source: proto.FetchAuthChallengeSourceServer, Origin: "example.test:8080"},
		{Source: proto.FetchAuthChallengeSourceServer, Origin: "http://example.test/path"},
		{Source: proto.FetchAuthChallengeSourceServer, Origin: "http://user@example.test"},
		{Source: proto.FetchAuthChallengeSourceServer, Origin: "http://example.test?query"},
		{Source: proto.FetchAuthChallengeSourceServer, Origin: "http://example.test#fragment"},
		{Source: proto.FetchAuthChallengeSourceServer, Origin: "http://:8080"},
		{Source: proto.FetchAuthChallengeSourceProxy, Origin: "socks5://proxy.example.test"},
		{AnyOrigin: true},
		{Source: proto.FetchAuthChallengeSourceProxy, Origin: "http://proxy.example.test", AnyOrigin: true},
	} {
		calls := 0
		browser := New().Context(t.Context()).Client(&hijackTestClient{call: func(context.Context, string, any) ([]byte, error) {
			calls++
			return []byte(`{}`), nil
		}})
		if err := browser.HandleAuth(credentials)(); !errors.Is(err, ErrAuthCredentials) || calls != 0 {
			t.Errorf("%+v: error %v after %d calls", credentials, err, calls)
		}
	}
	// Proxy URLs often carry credentials; errors must not repeat them.
	for _, origin := range []string{
		"http://proxy-user:S3cretPass@proxy.example.test:3128",
		"http://proxy-user:S3cret Pass@proxy.example.test:3128",
		"http://proxy-user:S3cret%zzPass@proxy.example.test:3128",
	} {
		browser := New().Context(t.Context()).Client(&hijackTestClient{call: func(context.Context, string, any) ([]byte, error) {
			return []byte(`{}`), nil
		}})
		err := browser.HandleAuth(AuthCredentials{Source: proto.FetchAuthChallengeSourceProxy, Origin: origin})()
		if !errors.Is(err, ErrAuthCredentials) || strings.Contains(err.Error(), "S3cret") {
			t.Errorf("error for an origin with credentials: %v", err)
		}
	}
	for raw, want := range map[string]string{
		"https://Example.test":      "https://example.test:443",
		"http://example.test:8080/": "http://example.test:8080",
		"http://[::1]:3128":         "http://[::1]:3128",
		"socks5://proxy.test:1080":  "socks5://proxy.test:1080",
	} {
		if got, err := normalizeAuthOrigin(raw); err != nil || got != want {
			t.Errorf("normalizeAuthOrigin(%q) = %q, %v", raw, got, err)
		}
	}
}
