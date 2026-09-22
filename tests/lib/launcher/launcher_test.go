package launcher_test

import (
	"flag"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/launcher/flags"
)

var setup = testutil.Setup(nil)

func TestLaunch(t *testing.T) {
	g := setup(t)

	defaults.Load()
	defaults.Proxy = "test.com"
	defer func() { defaults.ResetWith("") }()

	l := launcher.New().Preferences("").AlwaysOpenPDFExternally()
	t.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})
	u := l.MustLaunch()

	g.Regex(`\Aws://.+\z`, u)

	parsed, _ := url.Parse(u)

	{ // test GetWebSocketDebuggerURL
		for _, prefix := range []string{"", ":", "127.0.0.1:", "ws://127.0.0.1:"} {
			u2 := launcher.MustResolveURL(t.Context(), prefix+parsed.Port())
			g.Regex(u, u2)
		}

		_, err := launcher.ResolveURL(t.Context(), "")
		g.Err(err)
	}

	{
		_, err := launcher.NewManaged(t.Context(), "", "test-manager-token")
		g.Err(err)

		_, err = launcher.NewManaged(t.Context(), "1://", "test-manager-token")
		g.Err(err)

		_, err = launcher.NewManaged(t.Context(), "ws://not-exists", "test-manager-token")
		g.Err(err)
	}
}

func TestLaunchUserMode(t *testing.T) {
	g := setup(t)
	const port = 58472
	// Chrome must stop before TempDir removes the caller-owned profile.
	profile := t.TempDir()
	l := launcher.NewUserMode().Context(g.Context()).Headless(true).
		RemoteDebuggingPort(port).UserDataDir(profile)
	t.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})
	u := l.MustLaunch()
	g.Eq(u, launcher.NewUserMode().Context(g.Context()).RemoteDebuggingPort(port).MustLaunch())
}

func TestLaunchXVFB(t *testing.T) {
	l := launcher.New().XVFB()
	t.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})
	_, _ = l.Launch()
}

var testProfileDir = flag.Bool("test-profile-dir", false, "set it to test profile dir")

func TestProfileDir(t *testing.T) {
	g := setup(t)

	url := launcher.New().Headless(false).
		ProfileDir("").ProfileDir("test-profile-dir")

	if !*testProfileDir {
		g.Skip("It's not CI friendly, so we skip it!")
	}

	t.Cleanup(func() {
		url.Kill()
		url.Cleanup()
	})
	url.MustLaunch()

	userDataDir := url.Get(flags.UserDataDir)
	file, err := os.Stat(filepath.Join(userDataDir, "test-profile-dir"))

	g.E(err)
	g.True(file.IsDir())
}

func TestLaunchMultiTimes(t *testing.T) {
	g := setup(t)

	// first time launch, success.
	l := launcher.New()
	t.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})
	u, e := l.Launch()
	g.Neq(u, "")
	g.E(e)

	// second time launch, failed with ErrAlreadyLaunched.
	_, e = l.Launch()
	g.Eq(e, launcher.ErrAlreadyLaunched)
}
