package launcher

import (
	"errors"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/rah-0/rod/lib/launcher/flags"
)

func TestURLHelpersRejectSwitches(t *testing.T) {
	for _, value := range []string{"--user-agent=injected", " -flag", "\t/flag", "\u3000--flag", "\u00a0/flag", "\ufeff--flag", "relative/path", "javascript:alert(1)", "http://"} {
		t.Run(value, func(t *testing.T) {
			if err := Open(value); !errors.Is(err, ErrInvalidURL) {
				t.Fatalf("Open = %v", err)
			}
			if _, err := New().Bin("does-not-exist").StartURL(value).Launch(); !errors.Is(err, ErrInvalidURL) {
				t.Fatalf("Launch = %v", err)
			}
		})
	}
	for _, value := range []string{"https://example.com", "http://127.0.0.1/", "about:blank", "file:///tmp/example.html", "data:text/html,hello", "chrome://version"} {
		if err := validateURL(value); err != nil {
			t.Errorf("valid URL %q: %v", value, err)
		}
	}
}

func TestFormatArgsPreservesPositionalOrder(t *testing.T) {
	urls := []string{"https://z.example", "https://a.example"}
	args := New().Set(flags.Arguments, urls...).FormatArgs()
	if !slices.Equal(args[len(args)-len(urls):], urls) {
		t.Fatalf("positional order changed: %v", args)
	}
}

func TestSecureLauncherDefaults(t *testing.T) {
	l := New()
	for _, flag := range []flags.Flag{flags.NoSandbox, "disable-site-isolation-trials", "disable-popup-blocking", "disable-ipc-flooding-protection"} {
		if l.Has(flag) {
			t.Errorf("default weakens isolation: %s", flag)
		}
	}
	if strings.Contains(l.Get("disable-features"), "site-per-process") {
		t.Fatal("site isolation disabled")
	}
	if NewUserMode().Get(flags.RemoteDebuggingPort) != "0" {
		t.Fatal("user mode uses a predictable port")
	}
	l.NoSandbox(true)
	if !l.Has(flags.NoSandbox) {
		t.Fatal("explicit sandbox opt-out failed")
	}
}

func TestLaunchNeverAdoptsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := New().Bin("unused-browser").RemoteDebuggingPort(port).Launch(); !errors.Is(err, ErrDebuggingPortInUse) {
		t.Fatalf("Launch = %v, want occupied-port error", err)
	}
}
