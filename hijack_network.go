package rod

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

// DefaultMaxSafeHijackRequests bounds concurrent LoadResponse network requests
// across the process. Calls beyond the limit fail instead of queuing.
const DefaultMaxSafeHijackRequests = 8

// ErrHijackUnsafeClient means a custom HTTP client requires LoadResponseUnsafe.
var ErrHijackUnsafeClient = errors.New("rod: custom hijack clients require LoadResponseUnsafe")

// ErrHijackNetworkPolicy means a request or redirect violates LoadResponse's
// destination policy.
var ErrHijackNetworkPolicy = errors.New("rod: hijack destination blocked by network policy")

// ErrHijackConcurrency means the router's handler limit or the process-wide
// LoadResponse network limit has been reached.
var ErrHijackConcurrency = errors.New("rod: hijack concurrency limit exceeded")

var safeHijackRequests = make(chan struct{}, DefaultMaxSafeHijackRequests)

// Conservatively exclude special-purpose IPv4 and IPv6 destinations, including
// transition mechanisms that could encode a private IPv4 destination. The
// ordinary private, loopback, multicast and link-local ranges are rejected by
// netip's address predicates below. Deployment-specific public ranges belong
// in DenyNetworks or an external egress policy.
// See https://www.iana.org/assignments/iana-ipv4-special-registry and
// https://www.iana.org/assignments/iana-ipv6-special-registry.
var hijackSpecialNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("168.63.129.16/32"), // Azure platform virtual address.
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var hijackIPv6Global = netip.MustParsePrefix("2000::/3")

type hijackDialer struct {
	lookup func(context.Context, string, string) ([]netip.Addr, error)
	dial   func(context.Context, string, string) (net.Conn, error)
	deny   []netip.Prefix
}

func (d *hijackDialer) allowed(ip netip.Addr) bool {
	if ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || (ip.Is6() && !hijackIPv6Global.Contains(ip)) {
		return false
	}
	for _, prefix := range hijackSpecialNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	for _, prefix := range d.deny {
		if !prefix.IsValid() || prefix.Contains(ip) || (ip.Is4() && prefix.Contains(netip.AddrFrom16(ip.As16()))) {
			return false
		}
	}
	return true
}

func (d *hijackDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	// Bound resolution and all connection attempts together.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := d.lookup(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: no destination addresses", ErrHijackNetworkPolicy)
	}
	// Reject mixed public/private answers before making any connection. Dial
	// the validated numeric address, never the hostname, to avoid a second DNS
	// lookup and a check-to-use rebinding window.
	for _, ip := range addresses {
		if !d.allowed(ip) {
			return nil, fmt.Errorf("%w: %s", ErrHijackNetworkPolicy, ip)
		}
	}
	var dialErr error
	for _, ip := range addresses {
		conn, err := d.dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		dialErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, dialErr
}

func checkHijackURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ErrHijackNetworkPolicy
	}
	return nil
}

func hijackOrigin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

func newSafeHijackClient(deny []netip.Prefix) *http.Client {
	dialer := &hijackDialer{
		lookup: net.DefaultResolver.LookupNetIP,
		dial:   (&net.Dialer{}).DialContext,
		deny:   slices.Clone(deny),
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// A fresh direct connection for each hop enforces current DNS and
			// per-request policy even across redirects. No environment proxy.
			DialContext:            dialer.DialContext,
			DisableKeepAlives:      true,
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  10 * time.Second,
			ExpectContinueTimeout:  time.Second,
			MaxResponseHeaderBytes: DefaultMaxResponseHeaderBytes,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := checkHijackURL(req.URL); err != nil {
				return err
			}
			if len(via) >= 10 || len(via) == 0 || hijackOrigin(req.URL) != hijackOrigin(via[0].URL) {
				return fmt.Errorf("%w: redirect outside original origin or limit", ErrHijackNetworkPolicy)
			}
			return nil
		},
	}
}

func (h *Hijack) loadResponseSafe(loadBody bool) error {
	if err := checkHijackURL(h.Request.req.URL); err != nil {
		return err
	}
	select {
	case <-h.Request.req.Context().Done():
		return h.Request.req.Context().Err()
	default:
	}
	select {
	case safeHijackRequests <- struct{}{}:
		defer func() { <-safeHijackRequests }()
	default:
		return ErrHijackConcurrency
	}
	client := newSafeHijackClient(h.DenyNetworks)
	if h.MaxResponseHeaderBytes > 0 {
		client.Transport.(*http.Transport).MaxResponseHeaderBytes = h.MaxResponseHeaderBytes
	}
	defer client.CloseIdleConnections()
	return h.LoadResponseUnsafe(client, loadBody)
}
