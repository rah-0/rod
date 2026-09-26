package launcher

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/launcher/flags"
	"github.com/rah-0/rod/lib/utils"
)

var setup = testutil.Setup(nil)

const managerTestToken = "test-manager-token-0123456789abcdef0123456789abcdef"

func TestToHTTP(t *testing.T) {
	g := setup(t)

	u, _ := url.Parse("wss://a.com")
	g.Eq("https", toHTTP(*u).Scheme)

	u, _ = url.Parse("ws://a.com")
	g.Eq("http", toHTTP(*u).Scheme)
}

func TestToWS(t *testing.T) {
	g := setup(t)

	u, _ := url.Parse("https://a.com")
	g.Eq("wss", toWS(*u).Scheme)

	u, _ = url.Parse("http://a.com")
	g.Eq("ws", toWS(*u).Scheme)
}

func TestLaunchOptions(t *testing.T) {
	g := setup(t)

	defaults.Load()
	defaults.Show = true
	defaults.Devtools = true
	inContainer = true

	// restore
	defer func() {
		defaults.ResetWith("")
		inContainer = utils.InContainer
	}()

	l := New()

	g.False(l.Has(flags.Headless))

	g.True(l.Has(flags.NoSandbox))

	g.True(l.Has("auto-open-devtools-for-tabs"))
}

func TestGetURLErr(t *testing.T) {
	g := setup(t)

	l := New()

	l.ctxCancel()
	_, err := l.getURL()
	g.Err(err)

	l = New()
	l.parser.lock.Lock()
	l.parser.Buffer = "err"
	l.parser.lock.Unlock()
	close(l.exit)
	_, err = l.getURL()
	g.Eq("[launcher] Failed to get the debug url: err", err.Error())
}

func TestCleanupPreservesConfiguredUserDataDir(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	defaults.Load()
	previousDir := defaults.Dir
	defaults.Dir = dir
	t.Cleanup(func() { defaults.Dir = previousDir })

	l := New()
	close(l.exit)
	l.Cleanup()

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("explicit user data directory was removed: %v", err)
	}
}

func TestManaged(t *testing.T) {
	g := setup(t)

	ctx := g.Timeout(5 * time.Second)

	s := testutil.New(t).Serve()
	rl := NewManager(managerTestToken)
	rl.userDataRoot = t.TempDir()
	profilePath := make(chan string, 1)
	rl.BeforeLaunch = func(l *Launcher, _ http.ResponseWriter, _ *http.Request) {
		profilePath <- l.Get(flags.UserDataDir)
	}
	s.Mux.Handle("/", rl)

	l := MustNewManaged(t.Context(), s.URL(), managerTestToken)
	if l.Has("disable-http2") {
		t.Fatal("managed launcher retained the obsolete Docker HTTP/2 workaround")
	}
	c := l.MustClient()
	g.E(c.Call(ctx, "", "Browser.getVersion", nil))
	var dir string
	select {
	case dir = <-profilePath:
	case <-ctx.Done():
		t.Fatal("manager did not report its browser profile")
	}
	if rel, err := filepath.Rel(rl.userDataRoot, dir); err != nil || rel == "." || !filepath.IsLocal(rel) {
		t.Fatalf("managed profile %q is outside %q", dir, rl.userDataRoot)
	}
	g.E(os.Stat(dir))
	utils.Sleep(1)
	_, _ = c.Call(ctx, "", "Browser.crash", nil)

	for ctx.Err() == nil {
		utils.Sleep(0.1)
		_, err := os.Stat(dir)
		if err != nil {
			break
		}
	}
	g.Err(os.Stat(dir))

	u, h := MustNewManaged(t.Context(), s.URL(), managerTestToken).Bin("go").ClientHeader()
	_, err := cdp.StartWithURL(ctx, u, h)
	g.Eq(err.(*cdp.BadHandshakeError).Body, "[rod-manager] remote option is not allowed: rod-bin\n")
}

func TestLaunchErrs(t *testing.T) {
	g := setup(t)

	l := New().Bin("echo")
	_, err := l.Launch()
	g.Err(err)
}

func TestResolveBin(t *testing.T) {
	g := setup(t)

	searched := false
	bin, err := resolveBin("/configured/browser", func() (string, bool) {
		searched = true
		return "", false
	})
	g.E(err)
	g.Eq("/configured/browser", bin)
	g.False(searched)

	bin, err = resolveBin("", func() (string, bool) {
		return "/local/browser", true
	})
	g.E(err)
	g.Eq("/local/browser", bin)

	bin, err = resolveBin("", func() (string, bool) {
		return "", false
	})
	g.Eq("", bin)
	g.True(errors.Is(err, ErrBrowserNotFound))
}

func TestManagerBrowserBin(t *testing.T) {
	g := setup(t)

	defaults.Load()
	allowed := defaults.Bin
	m := NewManager(managerTestToken)
	m.validateLaunchOptions(New().Bin(allowed), httptest.NewRecorder())

	w := httptest.NewRecorder()
	g.Panic(func() {
		m.validateLaunchOptions(New().Bin(allowed+"-not-allowed"), w)
	})
	g.Has(w.Body.String(), "remote option is not allowed: rod-bin")
}

func TestManagerBrowserEnvironment(t *testing.T) {
	t.Setenv(ManagerTokenEnv, "do-not-inherit")
	t.Setenv("ROD_MANAGER_TEST_KEEP", "keep")

	l := New()
	l.Env("ROD_MANAGER_TEST_KEEP=override", ManagerTokenEnv+"=override")
	env := managerBrowserEnvironment(l)

	if slices.Contains(env, ManagerTokenEnv+"=override") {
		t.Fatal("manager token was retained in the configured browser environment")
	}
	if !slices.Contains(env, "ROD_MANAGER_TEST_KEEP=override") {
		t.Fatal("manager removed an unrelated configured browser environment value")
	}

	l.Delete(flags.Env)
	env = managerBrowserEnvironment(l)
	for _, value := range env {
		name, _, _ := strings.Cut(value, "=")
		if strings.EqualFold(name, ManagerTokenEnv) {
			t.Fatal("manager token was inherited from the process environment")
		}
	}
	if !slices.Contains(env, "ROD_MANAGER_TEST_KEEP=keep") {
		t.Fatal("manager removed an unrelated inherited browser environment value")
	}
}

func TestManagerRejectsRemoteProfilePaths(t *testing.T) {
	m := NewManager(managerTestToken)
	m.validateLaunchOptions(New().ProfileDir("Profile 1"), httptest.NewRecorder())
	m.validateLaunchOptions(New().UserDataDir(filepath.Join("..", "ignored")), httptest.NewRecorder())
	absoluteProfile, err := filepath.Abs("outside")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		configure func(*Launcher)
	}{
		{"profile traversal", func(l *Launcher) { l.ProfileDir(filepath.Join("..", "outside")) }},
		{"nested profile", func(l *Launcher) { l.ProfileDir(filepath.Join("nested", "profile")) }},
		{"absolute profile", func(l *Launcher) { l.ProfileDir(absoluteProfile) }},
		{"empty preferences values", func(l *Launcher) { l.Flags[flags.Preferences] = nil }},
		{"empty debugging port values", func(l *Launcher) { l.Flags[flags.RemoteDebuggingPort] = nil }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			l := New()
			test.configure(l)
			w := httptest.NewRecorder()

			deferredPanic := false
			func() {
				defer func() { deferredPanic = recover() != nil }()
				m.validateLaunchOptions(l, w)
			}()

			if !deferredPanic {
				t.Fatal("manager accepted an unsafe remote path")
			}
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestManagerOwnsProfileAndDebuggingPort(t *testing.T) {
	m := NewManager(managerTestToken)
	temp := t.TempDir()
	m.userDataRoot = filepath.Join(temp, "profiles")
	m.allowedBin = "echo"

	outsideSibling := m.userDataRoot + "-outside"
	outsideTraversal := filepath.Join(m.userDataRoot, "..", "outside")
	for _, dir := range []string{outsideSibling, outsideTraversal} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var launched *Launcher
	m.BeforeLaunch = func(l *Launcher, _ http.ResponseWriter, _ *http.Request) {
		launched = l
		l.UserDataDir(outsideTraversal).RemoteDebuggingPort(9223)
	}

	l := New().Bin("echo").UserDataDir(outsideSibling).RemoteDebuggingPort(9222)
	req := httptest.NewRequest(http.MethodGet, "http://manager", nil)
	req.Header.Set("Authorization", "Bearer "+managerTestToken)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set(HeaderName, string(l.JSON()))

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		m.ServeHTTP(httptest.NewRecorder(), req)
	}()
	if recovered == nil {
		t.Fatal("test browser unexpectedly produced a DevTools URL")
	}
	if launched == nil {
		t.Fatal("BeforeLaunch was not called")
	}
	if got := launched.Get(flags.RemoteDebuggingPort); got != "0" {
		t.Fatalf("debugging port = %q, want 0", got)
	}
	managedDir := launched.Get(flags.UserDataDir)
	if managedDir == outsideSibling || managedDir == outsideTraversal {
		t.Fatal("manager retained a client or hook supplied user data directory")
	}
	if rel, err := filepath.Rel(m.userDataRoot, managedDir); err != nil || rel == "." || !filepath.IsLocal(rel) {
		t.Fatalf("managed profile %q is outside %q", managedDir, m.userDataRoot)
	}
	if _, err := os.Stat(managedDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed profile was not removed: %v", err)
	}
	for _, dir := range []string{outsideSibling, outsideTraversal} {
		if _, err := os.Stat(filepath.Join(dir, "keep")); err != nil {
			t.Fatalf("outside directory %q was changed: %v", dir, err)
		}
	}
}

func TestManagerCleanupStaysInOpenedRoot(t *testing.T) {
	base := t.TempDir()
	actual := filepath.Join(base, "actual")
	victim := filepath.Join(base, "victim")
	alias := filepath.Join(base, "profiles")
	for _, dir := range []string{actual, victim} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(actual, alias); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}

	m := NewManager(managerTestToken)
	m.userDataRoot = alias
	profile, err := m.newManagedProfile()
	if err != nil {
		t.Fatal(err)
	}
	l := New().UserDataDir(profile.path).ProfileDir("Default").Set(flags.Preferences, `{}`)
	l.profileRoot = profile.root
	l.setupUserPreferences()

	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, alias); err != nil {
		t.Fatal(err)
	}
	victimProfile := filepath.Join(victim, profile.name)
	if err := os.Mkdir(victimProfile, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(victimProfile, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	m.cleanup(l, profile)
	if _, err := os.Stat(filepath.Join(actual, profile.name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original managed profile was not removed: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("cleanup escaped through the repointed symlink: %v", err)
	}
}

func TestManagerAuthentication(t *testing.T) {
	g := setup(t)

	m := NewManager(managerTestToken)
	defaultsCalled := false
	m.Defaults = func(_ http.ResponseWriter, r *http.Request) *Launcher {
		defaultsCalled = true
		g.Eq("", r.Header.Get("Authorization"))
		return New()
	}
	s := httptest.NewServer(m)
	defer s.Close()

	res, err := http.Get(s.URL)
	g.E(err)
	g.Eq(http.StatusUnauthorized, res.StatusCode)
	g.Eq("Bearer", res.Header.Get("WWW-Authenticate"))
	g.E(res.Body.Close())
	g.False(defaultsCalled)

	launchHookCalled := false
	m.BeforeLaunch = func(_ *Launcher, _ http.ResponseWriter, _ *http.Request) {
		launchHookCalled = true
	}
	upgrade := httptest.NewRequest(http.MethodGet, "http://manager", nil)
	upgrade.Header.Set("Upgrade", "websocket")
	upgrade.Header.Set(HeaderName, string(New().JSON()))
	upgradeResult := httptest.NewRecorder()
	m.ServeHTTP(upgradeResult, upgrade)
	g.Eq(http.StatusUnauthorized, upgradeResult.Code)
	g.False(launchHookCalled)

	_, err = NewManaged(t.Context(), s.URL, "wrong-token")
	g.True(errors.Is(err, ErrManagerUnauthorized))
	g.False(defaultsCalled)

	l, err := NewManaged(t.Context(), s.URL, managerTestToken)
	g.E(err)
	g.True(defaultsCalled)

	_, header := l.ClientHeader()
	g.Eq("Bearer "+managerTestToken, header.Get("Authorization"))
	g.False(strings.Contains(string(l.JSON()), managerTestToken))

	locked := httptest.NewServer(NewManager(""))
	defer locked.Close()
	_, err = NewManaged(t.Context(), locked.URL, managerTestToken)
	g.True(errors.Is(err, ErrManagerUnauthorized))
	_, err = NewManaged(t.Context(), "http://192.0.2.1:7317", managerTestToken)
	g.True(errors.Is(err, ErrManagerInsecureTransport))
	_, err = NewManaged(t.Context(), "ws://manager.example:7317", managerTestToken)
	g.True(errors.Is(err, ErrManagerInsecureTransport))

	redirectedAuthorization := ""
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		redirectedAuthorization = r.Header.Get("Authorization")
	}))
	defer redirectTarget.Close()
	redirectSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
	}))
	defer redirectSource.Close()
	_, err = NewManaged(t.Context(), redirectSource.URL, managerTestToken)
	g.Has(err.Error(), "HTTP status 302")
	g.Eq("", redirectedAuthorization)
}

func TestManagerRejectsRemoteProcessOptions(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		configure func(*Launcher)
	}{
		{"environment", func(l *Launcher) { l.Env("LD_PRELOAD=/tmp/library") }},
		{"working directory sibling prefix", func(l *Launcher) { l.WorkingDir(workingDir + "-outside") }},
		{"working directory traversal", func(l *Launcher) {
			l.WorkingDir(filepath.Join(workingDir, "..", "outside"))
		}},
		{"xvfb", func(l *Launcher) { l.XVFB("/bin/sh") }},
		{"argument smuggling", func(l *Launcher) {
			l.StartURL("--renderer-cmd-prefix=/bin/sh")
		}},
		{"windows argument smuggling", func(l *Launcher) {
			l.StartURL("/renderer-cmd-prefix=/bin/sh")
		}},
		// Chromium trims whitespace from each argument before it detects
		// switches: ASCII whitespace on POSIX, Unicode whitespace on Windows.
		{"space argument smuggling", func(l *Launcher) {
			l.StartURL(" --renderer-cmd-prefix=/bin/sh")
		}},
		{"tab argument smuggling", func(l *Launcher) {
			l.StartURL("\t--renderer-cmd-prefix=/bin/sh")
		}},
		{"newline argument smuggling", func(l *Launcher) {
			l.StartURL("\n--js-flags=--logfile=/tmp/v8.log")
		}},
		{"unicode space argument smuggling", func(l *Launcher) {
			l.StartURL("\u3000/renderer-cmd-prefix=/bin/sh")
		}},
		{"no-break space argument smuggling", func(l *Launcher) {
			l.StartURL("\u00a0-renderer-cmd-prefix=/bin/sh")
		}},
		{"format character argument smuggling", func(l *Launcher) {
			l.StartURL("\ufeff--renderer-cmd-prefix=/bin/sh")
		}},
		{"later argument smuggling", func(l *Launcher) {
			l.Set(flags.Arguments, "about:blank", " --renderer-cmd-prefix=/bin/sh")
		}},
		{"option smuggling", func(l *Launcher) {
			l.Flags["renderer-cmd-prefix=/bin/sh"] = nil
		}},
	}

	for _, f := range []flags.Flag{
		"allow-unsafe-devtools-remote-file-loading",
		"auth-negotiate-delegate-allowlist",
		"auth-negotiate-delegate-whitelist",
		"auth-server-allowlist",
		"auth-server-whitelist",
		"browser-subprocess-path",
		"clear-key-cdm-path-for-testing",
		"crash-dumps-dir",
		"custom-devtools-frontend",
		"disable-extensions-except",
		"disk-cache-dir",
		"dump-browser-histograms",
		"enable-logging",
		"enable-tracing",
		"enable-tracing-output",
		"export-ukm-logs-to-file",
		"export-uma-logs-to-file",
		"focus-result-file",
		"gpu-launcher",
		"gssapi-library-name",
		"install-isolated-web-app-from-file",
		"ipc-dump-directory",
		"js-flags",
		"list-apps",
		"load-and-launch-app",
		"load-apps",
		"load-component-extension",
		"load-extension",
		"log-file",
		"log-net-log",
		"nacl-gdb",
		"nacl-gdb-script",
		"nacl-loader-cmd-prefix",
		"ozone-dump-file",
		"pack-extension",
		"pack-extension-key",
		"plugin-launcher",
		"ppapi-flash-path",
		"ppapi-plugin-launcher",
		"preinstalled-web-apps-dir",
		"print-to-pdf",
		"profiling-file",
		"register-pepper-plugins",
		"remote-allow-origins",
		"remote-debugging-address",
		"remote-debugging-io-pipes",
		"remote-debugging-pipe",
		"renderer-cmd-prefix",
		"rod-unknown",
		"screenshot",
		"single-argument",
		"ssl-key-log-file",
		"trace-config-file",
		"trace-shutdown-file",
		"trace-startup",
		"trace-startup-file",
		"trace-to-file",
		"type",
		"utility-cmd-prefix",
		"webrtc-event-logging",
		"zygote-cmd-prefix",
		// Chromium lowercases switch names on Windows.
		"Renderer-Cmd-Prefix",
		"LOAD-EXTENSION",
		"Remote-Debugging-Address",
	} {
		tests = append(tests, struct {
			name      string
			configure func(*Launcher)
		}{string(f), func(l *Launcher) { l.Set(f, "/bin/sh") }})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewManager(managerTestToken)
			hookCalled := false
			m.BeforeLaunch = func(_ *Launcher, _ http.ResponseWriter, _ *http.Request) {
				hookCalled = true
			}

			l := New()
			test.configure(l)
			req := httptest.NewRequest(http.MethodGet, "http://manager", nil)
			req.Header.Set("Authorization", "Bearer "+managerTestToken)
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set(HeaderName, string(l.JSON()))
			w := httptest.NewRecorder()

			func() {
				defer func() { _ = recover() }()
				m.ServeHTTP(w, req)
			}()

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
			if hookCalled {
				t.Fatal("BeforeLaunch ran for a restricted remote option")
			}
		})
	}

	NewManager(managerTestToken).validateLaunchOptions(
		New().Set("disable-gpu"),
		httptest.NewRecorder(),
	)
}

func TestManagerRequiresCanonicalOptionNames(t *testing.T) {
	m := NewManager(managerTestToken)
	validate := func(f flags.Flag, values ...string) (int, bool) {
		l := New()
		l.Flags[f] = values
		w := httptest.NewRecorder()
		rejected := false
		func() {
			defer func() { rejected = recover() != nil }()
			m.validateLaunchOptions(l, w)
		}()
		return w.Code, rejected
	}

	for _, f := range []flags.Flag{
		// Chromium on Windows would treat these as server-owned or
		// restricted switches.
		"User-Data-Dir",
		"Remote-Debugging-Port",
		"Profile-Directory",
		"Rod-Env",
		"Js-Flags",
		"Disk-Cache-Dir",
		// Other non-canonical spellings.
		"-renderer-cmd-prefix",
		"renderer-cmd-prefix=/bin/sh",
		" renderer-cmd-prefix",
		"renderer-cmd-prefix ",
		"renderer_cmd_prefix",
		"-",
		"disableİgpu",
	} {
		t.Run(string(f), func(t *testing.T) {
			code, rejected := validate(f, "/bin/sh")
			if !rejected || code != http.StatusBadRequest {
				t.Fatalf("option was accepted: status %d", code)
			}
		})
	}

	for f, values := range map[flags.Flag][]string{
		"disable-gpu":             nil,
		"enable-features":         {"NetworkService"},
		"headless":                {"new"},
		"lang":                    {"en-US"},
		"no-sandbox":              nil,
		"proxy-server":            {"127.0.0.1:8080"},
		"window-size":             {"800", "600"},
		flags.Arguments:           {"about:blank", "https://example.com/-x", " about:blank"},
		flags.Preferences:         {`{}`},
		flags.ProfileDir:          {"Profile 1"},
		flags.RemoteDebuggingPort: {"9222"},
		flags.UserDataDir:         {"ignored"},
	} {
		t.Run(string(f), func(t *testing.T) {
			if code, rejected := validate(f, values...); rejected {
				t.Fatalf("canonical option was rejected: status %d", code)
			}
		})
	}
}

func TestManagerStripsControlHeaders(t *testing.T) {
	g := setup(t)

	m := NewManager(managerTestToken)
	m.BeforeLaunch = func(_ *Launcher, w http.ResponseWriter, r *http.Request) {
		g.Eq("", r.Header.Get("Authorization"))
		g.Eq("", r.Header.Get(HeaderName))
		rejectManagerLaunch(w, "[rod-manager] test stop")
	}

	l := New()
	req := httptest.NewRequest(http.MethodGet, "http://manager", nil)
	req.Header.Set("Authorization", "Bearer "+managerTestToken)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set(HeaderName, string(l.JSON()))

	g.Panic(func() {
		m.ServeHTTP(httptest.NewRecorder(), req)
	})
}

func TestURLParserErr(t *testing.T) {
	g := setup(t)

	u := &URLParser{
		Buffer: "error",
		lock:   &sync.Mutex{},
	}

	g.Eq(u.Err().Error(), "[launcher] Failed to get the debug url: error")

	u.Buffer = "/tmp/rod/chromium-818858/chrome: error while loading shared libraries: libgobject-2.0.so.0: cannot open shared object file: No such file or directory"
	g.Eq(u.Err().Error(), "[launcher] Failed to launch the browser: /tmp/rod/chromium-818858/chrome: error while loading shared libraries: libgobject-2.0.so.0: cannot open shared object file: No such file or directory")
}

func TestTestOpen(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "chrome"), []byte("unused"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	called := false
	openExec = func(_ string, _ ...string) *exec.Cmd {
		called = true
		return exec.Command(filepath.Join(binDir, "not-exists"))
	}
	defer func() { openExec = exec.Command }()

	Open("about:blank")
	if !called && runtime.GOOS != "windows" {
		t.Fatal("Open did not attempt to launch the discovered browser")
	}
}

func TestLaunchClient(t *testing.T) {
	g := setup(t)

	ctx := g.Timeout(5 * time.Second)

	s := testutil.New(t).Serve()
	rl := NewManager(managerTestToken)
	s.Mux.Handle("/", rl)

	l := MustNewManaged(t.Context(), s.URL(), managerTestToken)
	c, err := l.Client()
	if err != nil {
		g.Err(err)
	}
	g.E(c.Call(ctx, "", "Browser.getVersion", nil))
	g.E(c.Call(ctx, "", "Browser.close", nil))
}
