package rod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
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

func TestHijackRepeatedHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "session=one")
		w.Header().Add("Set-Cookie", "csrf=two")
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	h := newHijackRouter(New(), nil).new(t.Context(), &proto.FetchRequestPaused{
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
				h := newHijackRouter(New(), nil).new(t.Context(), event)
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
				if err := h.LoadResponse(server.Client(), true); err != nil {
					t.Fatal(err)
				}
				if h.Response.Payload().ResponseCode != 200 || !bytes.Equal([]byte(h.Response.Body()), want) {
					t.Fatalf("redirect = %d, %x", h.Response.Payload().ResponseCode, h.Response.Body())
				}
				if len(observed) != 2 {
					t.Fatalf("server received %d requests", len(observed))
				}
				if first, second := <-observed, <-observed; !bytes.Equal(first, want) || !bytes.Equal(second, want) {
					t.Fatalf("server bodies = %x, %x", first, second)
				}
			})
		}
	}
	without := newHijackRouter(New(), nil).new(t.Context(), &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: "http://example.test", Method: http.MethodGet}})
	if without.Request.Req().Body != nil || without.Request.Req().GetBody != nil {
		t.Fatal("invented an absent body")
	}
	without.Request.SetBody("")
	if without.Request.Req().Body != http.NoBody {
		t.Fatal("empty replacement is not explicit")
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

func TestHijackRouteDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var actions []string
		client := &hijackTestClient{call: func(_ context.Context, method string, _ any) ([]byte, error) {
			if method == "Fetch.fulfillRequest" || method == "Fetch.continueRequest" {
				actions = append(actions, method)
			}
			return []byte(`{}`), nil
		}}
		browser := New().Context(ctx).Client(client)
		browser.event = observable.New[*Message](ctx)
		router := browser.HijackRequests()
		seen := []string{}
		if err := router.Add("*", proto.NetworkResourceTypeImage, func(*Hijack) { seen = append(seen, "wrong") }); err != nil {
			t.Fatal(err)
		}
		if err := router.Add("*", proto.NetworkResourceTypeDocument, func(h *Hijack) { seen = append(seen, "skip"); h.Skip = true }); err != nil {
			t.Fatal(err)
		}
		if err := router.Add("*", proto.NetworkResourceTypeDocument, func(h *Hijack) { seen = append(seen, "fulfill"); h.Response.SetBody("ok") }); err != nil {
			t.Fatal(err)
		}
		if err := router.Add("*", "", func(*Hijack) { seen = append(seen, "late") }); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- router.Run() }()
		publish := func(kind proto.NetworkResourceType) {
			data, err := json.Marshal(proto.FetchRequestPaused{RequestID: "one", ResourceType: kind, Request: &proto.NetworkRequest{URL: "http://example.test/", Method: http.MethodGet}})
			if err != nil {
				t.Fatal(err)
			}
			browser.event.Publish(&Message{Method: "Fetch.requestPaused", data: data})
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
		if err := router.Add("after-stop", "", func(*Hijack) {}); err == nil {
			t.Fatal("stopped router enabled Fetch again")
		}
	})
}

func TestHijackLifecycleErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupErr, cleanupErr := errors.New("setup failed"), errors.New("cleanup failed")
		browser := New().Context(t.Context()).Client(&hijackTestClient{call: func(ctx context.Context, method string, _ any) ([]byte, error) {
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
		if err := router.Add("*", "", func(*Hijack) {}); !errors.Is(err, setupErr) {
			t.Fatal(err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		browser := New().Context(ctx).Client(&hijackTestClient{call: func(ctx context.Context, method string, _ any) ([]byte, error) {
			if method == "Fetch.disable" && ctx.Err() != nil {
				t.Error("cleanup inherited cancellation")
			}
			return []byte(`{}`), nil
		}})
		browser.event = observable.New[*Message](ctx)
		router := browser.HijackRequests()
		cancel()
		if err := router.Run(); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
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
				wait := browser.HandleAuth("user", "password")
				if phase == "wait" {
					cancel()
				} else if phase != "setup" {
					browser.event.Publish(&Message{Method: "Fetch.requestPaused", data: []byte(`{"requestId":"one"}`)})
					browser.event.Publish(&Message{Method: "Fetch.authRequired", data: []byte(`{"requestId":"one"}`)})
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
