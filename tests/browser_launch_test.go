package rod_test

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/launcher/flags"
	"github.com/rah-0/rod/lib/proto"
)

// The launch fixture accepts browser flags before the testing flag parser runs.
// Its environment is passed only to the browser child, not the Unix supervisor.
func init() {
	role := os.Getenv("ROD_OWNED_LAUNCH_FIXTURE")
	if role == "" {
		return
	}
	for _, arg := range os.Args[1:] {
		if dir, ok := strings.CutPrefix(arg, "--user-data-dir="); ok {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				os.Exit(2)
			}
		}
	}
	if role == "exit" {
		_, _ = fmt.Fprintln(os.Stderr, "fixture startup failed")
		os.Exit(3)
	}
	if role == "cancel" {
		_, _ = fmt.Fprintln(os.Stdout, "fixture ready")
	} else {
		endpoint := strings.Replace(os.Getenv("ROD_OWNED_LAUNCH_ENDPOINT"), "http://", "ws://", 1)
		_, _ = fmt.Fprintf(os.Stderr, "DevTools listening on %s/devtools/browser/fixture\nafter endpoint diagnostic\n", endpoint)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func ownedLaunchFixture(t *testing.T, role, endpoint string) *launcher.Launcher {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	l := launcher.New().Bin(bin).Env(append(os.Environ(), "ROD_OWNED_LAUNCH_FIXTURE="+role, "ROD_OWNED_LAUNCH_ENDPOINT="+endpoint)...)
	t.Cleanup(func() {
		l.Kill()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.CleanupContext(ctx); err != nil {
			t.Error(err)
		}
	})
	return l
}

func TestConfiguredBrowserLaunchFailures(t *testing.T) {
	for _, name := range []string{"missing executable", "early exit", "malformed endpoint", "invalid discovery", "connection failure", "cancel startup"} {
		for _, callerProfile := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/caller-profile=%t", name, callerProfile), func(t *testing.T) {
				var server *httptest.Server
				if name == "invalid discovery" || name == "connection failure" {
					server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if name == "invalid discovery" {
							_, _ = fmt.Fprint(w, `{"webSocketDebuggerUrl":"invalid"}`)
							return
						}
						if r.URL.Path == "/json/version" {
							_, _ = fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://%s/socket"}`, r.Host)
							return
						}
						http.Error(w, "fixture websocket failure", http.StatusServiceUnavailable)
					}))
					defer server.Close()
				}
				endpoint := ""
				if name == "malformed endpoint" {
					endpoint = "http://[bad"
				}
				if server != nil {
					endpoint = server.URL
				}
				role := "endpoint"
				if name == "early exit" {
					role = "exit"
				}
				if name == "cancel startup" {
					role = "cancel"
				}
				// Register the profile's cleanup before process cleanup.
				dir := t.TempDir()
				l := ownedLaunchFixture(t, role, endpoint)
				if name == "missing executable" {
					l.Bin(filepath.Join(dir, "missing"))
				}
				var marker string
				if callerProfile {
					l.UserDataDir(dir)
					marker = filepath.Join(dir, "keep")
					if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				if name == "cancel startup" {
					l.Logger(launchCallbackWriter{write: func([]byte) { cancel() }})
				}
				browser := rod.New().ControlURL("").Context(ctx)
				err := browser.Launch(l)
				if err == nil {
					t.Fatal("fixture unexpectedly connected")
				}
				switch name {
				case "missing executable":
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("lost executable error: %v", err)
					}
				case "early exit":
					if !strings.Contains(err.Error(), "fixture startup failed") {
						t.Fatal(err)
					}
				case "malformed endpoint":
					if !strings.Contains(err.Error(), "parse browser DevTools endpoint") {
						t.Fatal(err)
					}
				case "invalid discovery":
					if !strings.Contains(err.Error(), "invalid browser WebSocket URL") {
						t.Fatal(err)
					}
				case "connection failure":
					var handshake *cdp.BadHandshakeError
					if !errors.As(err, &handshake) {
						t.Fatalf("lost handshake error: %v", err)
					}
				case "cancel startup":
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("lost cancellation: %v", err)
					}
				}
				if server != nil && !strings.Contains(err.Error(), "after endpoint diagnostic") {
					t.Fatalf("lost post-endpoint output: %v", err)
				}
				if callerProfile {
					if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
						t.Fatalf("changed caller profile: %q, %v", data, err)
					}
				} else if _, err := os.Stat(l.Get(flags.UserDataDir)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("owned profile remains: %v", err)
				}
			})
		}
	}
}

type launchCallbackWriter struct{ write func([]byte) }

func (w launchCallbackWriter) Write(p []byte) (int, error) { w.write(p); return len(p), nil }

func TestConfiguredBrowserLaunchConflicts(t *testing.T) {
	for _, browser := range []*rod.Browser{rod.New().ControlURL("ws://127.0.0.1:1"), rod.New().ControlURL("").Client(&eventTestClient{}), func() *rod.Browser { b := rod.New().ControlURL(""); b.BrowserContextID = "incognito"; return b }()} {
		l := launcher.New().Bin(filepath.Join(t.TempDir(), "missing"))
		if err := browser.Launch(l); !errors.Is(err, rod.ErrLaunchConflict) {
			t.Fatal(err)
		}
		// Reject configuration before consuming or mutating the launcher.
		if _, err := l.LaunchNew(t.Context()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("conflict consumed launcher: %v", err)
		}
	}
	if err := rod.New().ControlURL("").Launch(nil); !errors.Is(err, rod.ErrLaunchConflict) {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://%s/socket"}`, r.Host)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	l := launcher.New().Bin("unused").RemoteDebuggingPort(port)
	if err := rod.New().ControlURL("").Launch(l); !errors.Is(err, launcher.ErrDebuggingPortInUse) {
		t.Fatalf("occupied port = %v", err)
	}
	if l.PID() != 0 {
		t.Fatal("occupied port launch owns a process")
	}
	if _, err := launcher.ResolveURL(t.Context(), server.URL); err != nil {
		t.Fatalf("unrelated browser was affected: %v", err)
	}
	used := launcher.New().Bin("unused").RemoteDebuggingPort(port)
	if _, err := used.Launch(); err != nil {
		t.Fatal(err)
	}
	if err := rod.New().ControlURL("").Launch(used); !errors.Is(err, launcher.ErrAlreadyLaunched) {
		t.Fatalf("used launcher = %v", err)
	}
}

func TestConfiguredBrowserLaunch(t *testing.T) {
	bin, found := launcher.LookPath()
	if !found {
		t.Skip("an installed browser is required")
	}
	for _, callerProfile := range []bool{false, true} {
		t.Run(fmt.Sprintf("caller-profile=%t", callerProfile), func(t *testing.T) {
			dir := t.TempDir()
			l := launcher.New().Bin(bin).Set("lang", "en-US").NoSandbox(true)
			marker := filepath.Join(dir, "keep")
			if callerProfile {
				l.UserDataDir(dir)
				if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			browser := rod.New().ControlURL("")
			if err := browser.Launch(l); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				l.Kill()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = l.CleanupContext(ctx)
			})
			result, err := proto.BrowserGetBrowserCommandLine{}.Call(browser)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(result.Arguments, " "), "--lang=en-US") {
				t.Fatal("configured option was lost")
			}
			incognito, err := browser.Incognito()
			if err != nil {
				t.Fatal(err)
			}
			if err := incognito.CloseWithTimeout(time.Second); err != nil {
				t.Fatal(err)
			}
			if _, err := browser.Version(); err != nil {
				t.Fatalf("incognito stopped parent: %v", err)
			}
			expired, cancel := context.WithCancel(t.Context())
			cancel()
			var wg sync.WaitGroup
			for range 3 {
				wg.Go(func() {
					if err := browser.Context(expired).CloseWithTimeout(5 * time.Second); err != nil {
						t.Errorf("concurrent close: %v", err)
					}
				})
			}
			wg.Wait()
			if err := browser.CloseWithTimeout(time.Second); err != nil {
				t.Fatalf("repeated close: %v", err)
			}
			if callerProfile {
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("caller profile removed: %v", err)
				}
			} else if _, err := os.Stat(l.Get(flags.UserDataDir)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owned profile remains: %v", err)
			}
		})
	}
}

func TestConfiguredBrowserOutputLimit(t *testing.T) {
	for _, limit := range []int{0, 8} {
		l := ownedLaunchFixture(t, "exit", "").OutputTail(limit)
		err := rod.New().ControlURL("").Launch(l)
		if !errors.Is(err, launcher.ErrDevToolsUnavailable) {
			t.Fatalf("lost launch cause: %v", err)
		}
		if strings.Contains(err.Error(), "fixture startup") {
			t.Fatalf("error escaped configured output limit: %v", err)
		}
		if limit != 0 && !strings.Contains(err.Error(), "failed") {
			t.Fatalf("missing recent output: %v", err)
		}
		if len(l.Output()) > limit {
			t.Fatal("tail exceeded limit")
		}
	}
}

func TestConfiguredBrowserDiscoveryFailureClosesTransport(t *testing.T) {
	closed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			_, _ = fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://%s/socket"}`, r.Host)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
		if err := rw.Flush(); err != nil {
			closed <- err
			return
		}
		var header [2]byte
		if _, err := io.ReadFull(rw, header[:]); err != nil {
			closed <- err
			return
		}
		size := int(header[1] & 127)
		if size >= 126 {
			closed <- fmt.Errorf("unexpected fixture request size %d", size)
			return
		}
		if _, err := io.ReadFull(rw, make([]byte, 4+size)); err != nil {
			closed <- err
			return
		}
		message := []byte(`{"id":1,"error":{"code":-32000,"message":"fixture discovery failure"}}`)
		if _, err := conn.Write(append([]byte{0x81, byte(len(message))}, message...)); err != nil {
			closed <- err
			return
		}
		_, err = conn.Read(make([]byte, 1))
		if !errors.Is(err, io.EOF) {
			closed <- fmt.Errorf("failed connection transport remained open: %w", err)
			return
		}
		closed <- nil
	}))
	defer server.Close()
	l := ownedLaunchFixture(t, "endpoint", server.URL)
	if err := rod.New().ControlURL("").Launch(l); err == nil || !strings.Contains(err.Error(), "fixture discovery failure") {
		t.Fatalf("discovery = %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}
