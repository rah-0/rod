package rod

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

func TestHijackNetworkAddresses(t *testing.T) {
	d := &hijackDialer{}
	for _, address := range []string{
		"0.0.0.0", "0.1.2.3", "10.1.2.3", "127.0.0.1", "169.254.169.254",
		"172.16.0.1", "192.168.1.1", "100.64.0.1", "100.100.100.200",
		"168.63.129.16", "192.0.0.1", "192.0.2.1", "192.88.99.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "fc00::1",
		"fe80::1", "ff02::1", "64:ff9b::7f00:1", "64:ff9b:1::1",
		"2001::1", "2001:db8::1", "2002:7f00:1::1", "3fff::1", "2001:4860::1%zone",
	} {
		if d.allowed(netip.MustParseAddr(address)) {
			t.Errorf("allowed special address %s", address)
		}
	}
	for _, address := range []string{"8.8.8.8", "1.1.1.1", "::ffff:8.8.8.8", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		if !d.allowed(netip.MustParseAddr(address)) {
			t.Errorf("rejected public address %s", address)
		}
	}
	d.deny = []netip.Prefix{netip.MustParsePrefix("8.8.8.0/24")}
	if d.allowed(netip.MustParseAddr("::ffff:8.8.8.8")) {
		t.Fatal("mapped address bypassed deployment policy")
	}
	d.deny = []netip.Prefix{netip.MustParsePrefix("::ffff:8.8.8.0/120")}
	if d.allowed(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("IPv4 address bypassed mapped deployment policy")
	}
	d.deny = []netip.Prefix{{}}
	if d.allowed(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("invalid deployment policy did not fail closed")
	}
}

func TestHijackNetworkPinsDNS(t *testing.T) {
	answers := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}
	var addresses []string
	lookups := 0
	dialErr := errors.New("no actual network connection")
	d := &hijackDialer{
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			lookups++
			if lookups > 1 {
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			}
			return answers, nil
		},
		dial: func(_ context.Context, _, address string) (net.Conn, error) {
			addresses = append(addresses, address)
			return nil, dialErr
		},
	}
	if _, err := d.DialContext(t.Context(), "tcp", "rebind.test:443"); !errors.Is(err, dialErr) {
		t.Fatal(err)
	}
	if lookups != 1 || !slices.Equal(addresses, []string{"8.8.8.8:443", "1.1.1.1:443"}) {
		t.Fatalf("lookups = %d, dialed = %v", lookups, addresses)
	}
	// A fresh connection revalidates DNS instead of reusing an earlier approval.
	if _, err := d.DialContext(t.Context(), "tcp", "rebind.test:443"); !errors.Is(err, ErrHijackNetworkPolicy) || len(addresses) != 2 {
		t.Fatalf("rebound address: %v, dialed %v", err, addresses)
	}
	// One disallowed answer blocks the entire result before dialing a public one.
	lookups = 0
	addresses = nil
	answers[1] = netip.MustParseAddr("127.0.0.1")
	if _, err := d.DialContext(t.Context(), "tcp", "mixed.test:80"); !errors.Is(err, ErrHijackNetworkPolicy) || len(addresses) != 0 {
		t.Fatalf("mixed addresses: %v, dialed %v", err, addresses)
	}
}

func TestHijackLoadResponseSafeDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("safe client reached an internal server")
	}))
	defer server.Close()
	for _, target := range []string{server.URL, "http://[::1]/", "file:///etc/passwd", "http://user:pass@example.test/"} {
		h := newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: target, Method: http.MethodGet}})
		if err := h.LoadResponse(nil, true); !errors.Is(err, ErrHijackNetworkPolicy) || h.Response.RawResponse != nil {
			t.Fatalf("%s: %v", target, err)
		}
	}
	h := newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: server.URL, Method: http.MethodGet}})
	if err := h.LoadResponse(http.DefaultClient, true); !errors.Is(err, ErrHijackUnsafeClient) {
		t.Fatalf("custom client bypassed explicit opt-in: %v", err)
	}
	if err := Try(h.MustLoadResponse); !errors.Is(err, ErrHijackNetworkPolicy) {
		t.Fatalf("MustLoadResponse bypassed safe defaults: %v", err)
	}
}

func TestHijackNetworkRedirects(t *testing.T) {
	client := newSafeHijackClient(nil)
	defer client.CloseIdleConnections()
	original, _ := http.NewRequest(http.MethodGet, "https://example.test/start", nil)
	for _, target := range []string{
		"http://example.test/", "https://sub.example.test/", "https://other.test/",
		"https://example.test:8443/", "https://user:pass@example.test/", "http://127.0.0.1/",
	} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if err := client.CheckRedirect(req, []*http.Request{original}); !errors.Is(err, ErrHijackNetworkPolicy) {
			t.Errorf("accepted redirect %s: %v", target, err)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://EXAMPLE.test:443/finish", nil)
	if err := client.CheckRedirect(req, []*http.Request{original}); err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(req, slices.Repeat([]*http.Request{original}, 10)); !errors.Is(err, ErrHijackNetworkPolicy) {
		t.Fatalf("redirect loop accepted: %v", err)
	}
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil || !transport.DisableKeepAlives || transport.TLSClientConfig != nil {
		t.Fatal("safe transport permits proxies, reuse across DNS changes, or custom TLS")
	}
}

func TestHijackNetworkTimeouts(t *testing.T) {
	for _, stage := range []string{"headers", "body"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := newSafeHijackClient(nil)
				defer client.CloseIdleConnections()
				transport := client.Transport.(*http.Transport)
				transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
					conn, peer := net.Pipe()
					go func() {
						defer peer.Close()
						buf := make([]byte, 4096)
						_, _ = peer.Read(buf)
						if stage == "body" {
							_, _ = io.WriteString(peer, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n")
						}
						_, _ = peer.Read(buf) // released when the client's timeout closes the connection
					}()
					return conn, nil
				}
				h := newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: "https://example.test/", Method: http.MethodGet}})
				// HTTP avoids TLS in this deterministic slow-server fixture.
				h.Request.req.URL.Scheme = "http"
				start := time.Now()
				err := h.LoadResponseUnsafe(client, true)
				if err == nil || !strings.Contains(err.Error(), "timeout") && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("stalled %s: %v", stage, err)
				}
				want := 10 * time.Second
				if stage == "body" {
					want = 30 * time.Second
				}
				if elapsed := time.Since(start); elapsed != want || h.Response.RawResponse != nil {
					t.Fatalf("stalled %s elapsed %v, want %v; response %v", stage, elapsed, want, h.Response.RawResponse)
				}
			})
		})
	}
}

func TestHijackNetworkConcurrencyAndCancellation(t *testing.T) {
	for range DefaultMaxSafeHijackRequests {
		safeHijackRequests <- struct{}{}
	}
	defer func() {
		for range DefaultMaxSafeHijackRequests {
			<-safeHijackRequests
		}
	}()
	h := newTestHijack(t, &proto.FetchRequestPaused{Request: &proto.NetworkRequest{URL: "https://example.test/", Method: http.MethodGet}})
	if err := h.LoadResponse(nil, true); !errors.Is(err, ErrHijackConcurrency) {
		t.Fatalf("concurrency limit: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	h.Request.req = h.Request.req.WithContext(ctx)
	if err := h.LoadResponse(nil, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestHijackRouterConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newEventTestBrowser(t)
		router := newHijackRouter(browser, browser).MaxConcurrentRequests(1)
		errs := make(chan error, 1)
		router.OnError(func(err error) { errs <- err })
		release := make(chan struct{})
		if err := router.Add("*", "", func(h *Hijack) {
			<-release
			h.Response.SetBody("ok")
		}); err != nil {
			t.Fatal(err)
		}
		paused := &proto.FetchRequestPaused{RequestID: "request", Request: &proto.NetworkRequest{URL: "https://example.test/", Method: http.MethodGet}}
		router.serveAsync(t.Context(), paused)
		synctest.Wait()
		router.serveAsync(t.Context(), paused)
		if err := <-errs; !errors.Is(err, ErrHijackConcurrency) {
			t.Fatalf("handler admission: %v", err)
		}
		calls := client.snapshot()
		if len(calls) != 2 || calls[1].method != "Fetch.failRequest" {
			t.Fatalf("excess request not failed: %v", calls)
		}
		close(release)
		synctest.Wait()
		if router.active != 0 {
			t.Fatal("handler slot was not released")
		}
		router.serveAsync(t.Context(), paused)
		synctest.Wait()
		calls = client.snapshot()
		if len(calls) != 4 || calls[2].method != "Fetch.fulfillRequest" || calls[3].method != "Fetch.fulfillRequest" {
			t.Fatalf("admitted requests: %v", calls)
		}
	})
}
