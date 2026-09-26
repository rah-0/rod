package rod

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/utils"
)

func TestMonitorHostAllowed(t *testing.T) {
	anyIPv6 := &net.TCPAddr{IP: net.IPv6unspecified, Port: 9223}
	anyIPv4 := &net.TCPAddr{IP: net.IPv4zero, Port: 9223}
	lan := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 9223}
	mappedLAN := &net.TCPAddr{IP: net.ParseIP("::ffff:192.0.2.10"), Port: 9223}
	linkLocal := &net.TCPAddr{IP: net.ParseIP("fe80::1"), Port: 9223, Zone: "eth0"}
	web := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 80}

	for _, c := range []struct {
		host  string
		addrs []net.Addr
		want  bool
	}{
		{"localhost", nil, true},
		{"localhost:1", nil, true},
		{"LocalHost.:9223", nil, true},
		{"127.0.0.1:9223", nil, true},
		{"127.1.2.3", nil, true},
		{"[::1]:9223", nil, true},
		{"[::ffff:127.0.0.1]:9223", nil, true},
		{"[::]:9223", []net.Addr{anyIPv6}, true},
		{"0.0.0.0:9223", []net.Addr{anyIPv4}, true},
		{"192.0.2.10:9223", []net.Addr{anyIPv6, lan}, true},
		{"192.0.2.10:9223", []net.Addr{anyIPv6, mappedLAN}, true},
		{"[fe80::1%25eth0]:9223", []net.Addr{anyIPv6, linkLocal}, true},
		{"192.0.2.10", []net.Addr{web}, true},
		{"192.0.2.10:", []net.Addr{web}, true},

		{"", []net.Addr{lan}, false},
		{"rebind.example:9223", []net.Addr{lan}, false},
		{"rebind.example", []net.Addr{web}, false},
		{"localhost.rebind.example", nil, false},
		{"127.0.0.1.rebind.example", nil, false},
		{"[::]:9223", []net.Addr{anyIPv4}, false},
		{"0.0.0.0:9223", nil, false},
		{"192.0.2.10:9224", []net.Addr{lan}, false},
		{"192.0.2.10", []net.Addr{lan}, false},
		{"192.0.2.11:9223", []net.Addr{anyIPv6, lan}, false},
		{"192.0.2.10:9223", []net.Addr{nil, &net.UnixAddr{Name: "monitor"}}, false},
	} {
		if got := monitorHostAllowed(c.host, c.addrs...); got != c.want {
			t.Errorf("monitorHostAllowed(%q, %v) = %v, want %v", c.host, c.addrs, got, c.want)
		}
	}
}

// monitorClient answers the calls that Connect makes.
type monitorClient struct{}

func (monitorClient) Event() <-chan *cdp.Event { return nil }

func (monitorClient) Call(context.Context, string, string, any) ([]byte, error) {
	return []byte(`{}`), nil
}

func TestMonitorReportsURL(t *testing.T) {
	var opened []string
	openMonitor = func(u string) { opened = append(opened, u) }
	defer func() { openMonitor = launcher.Open }()

	var logged [][]any
	browser := New().Context(t.Context()).Client(monitorClient{}).Monitor("127.0.0.1:0").
		Logger(utils.Log(func(list ...any) { logged = append(logged, list) }))
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}

	if len(logged) != 1 || len(logged[0]) != 2 || logged[0][0] != TraceTypeMonitor {
		t.Fatalf("Connect did not log the monitor URL: %v", logged)
	}
	monitor, _ := logged[0][1].(string)
	if len(opened) != 1 || opened[0] != monitor {
		t.Fatalf("Connect opened %q, but logged %q", opened, monitor)
	}
	u, err := url.Parse(monitor)
	if err != nil {
		t.Fatal(err)
	}

	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, c := range []struct {
		url    string
		status int
	}{
		{monitor, http.StatusOK},
		{"http://" + u.Host + "/", http.StatusNotFound},
	} {
		res, err := client.Get(c.url)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != c.status || (c.status == http.StatusOK) != strings.Contains(string(body), "Rod Monitor") {
			t.Fatalf("GET %s returned %s, want %d: %s", c.url, res.Status, c.status, body)
		}
	}
}
