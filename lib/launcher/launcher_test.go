package launcher_test

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

	{
		g.Panic(func() { launcher.New().Set("a=b") })
	}
}

func TestLaunchUserMode(t *testing.T) {
	g := setup(t)

	l := launcher.NewUserMode()
	l.Kill() // empty kill should do nothing

	has := l.Has("not-exists")
	g.False(has)

	l.Append("test-append", "a")
	f := l.Get("test-append")
	g.Eq("a", f)

	dir := l.Get(flags.UserDataDir)
	port := 58472

	l = l.Context(g.Context()).Delete("test").Bin("").
		Logger(io.Discard).
		HeadlessNew(true).HeadlessNew(false).
		Headless(false).Headless(true).RemoteDebuggingPort(port).
		NoSandbox(true).NoSandbox(false).
		Devtools(true).Devtools(false).
		StartURL("about:blank").
		Proxy("test.com").
		UserDataDir("test").UserDataDir(dir).
		WorkingDir("").
		Env(append(os.Environ(), "TZ=Asia/Tokyo")...)

	g.Eq(l.FormatArgs(), []string /* len=6 cap=8 */ {
		"--headless",
		`--no-startup-window`,           /* len=19 */
		`--proxy-server=test.com`,       /* len=23 */
		`--remote-debugging-port=58472`, /* len=29 */
		"--test-append=a",
		"about:blank",
	})

	// Create the profile before registering process cleanup: testing runs
	// cleanups in reverse order, and Chrome must stop before TempDir is removed.
	profile := t.TempDir()
	t.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})
	// Exercise the user-mode preset without opening the developer's profile.
	url := l.UserDataDir(profile).MustLaunch()

	g.Eq(url, launcher.NewUserMode().RemoteDebuggingPort(port).MustLaunch())
}

func TestUserModeErr(t *testing.T) {
	g := setup(t)

	_, err := launcher.NewUserMode().RemoteDebuggingPort(48277).Bin("not-exists").Launch()
	g.Err(err)

	_, err = launcher.NewUserMode().RemoteDebuggingPort(58217).Bin("echo").Launch()
	g.Err(err)
}

func TestAppMode(t *testing.T) {
	g := setup(t)

	l := launcher.NewAppMode("http://example.com")

	g.Eq(l.Get(flags.App), "http://example.com")
}

func TestGetWebSocketDebuggerURLErr(t *testing.T) {
	g := setup(t)

	_, err := launcher.ResolveURL(t.Context(), "1://")
	g.Err(err)
}

func TestLaunchErr(t *testing.T) {
	g := setup(t)

	g.Panic(func() {
		launcher.New().Bin("not-exists").MustLaunch()
	})
	g.Panic(func() {
		launcher.New().Headless(false).Bin("not-exists").MustLaunch()
	})
	g.Panic(func() {
		launcher.New().ClientHeader()
	})
	{
		l := launcher.New().XVFB()
		t.Cleanup(func() {
			l.Kill()
			l.Cleanup()
		})
		_, _ = l.Launch()
	}
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

func TestIgnoreCerts(t *testing.T) {
	g := setup(t)

	// https://travistidwell.com/jsencrypt/demo/
	testData := []string{
		`-----BEGIN PUBLIC KEY-----
MIGeMA0GCSqGSIb3DQEBAQUAA4GMADCBiAKBgF9pr2zok5bivQIEUN7Y58a9uB1o
sroMt3hxNfzOh/G+sXgYPPoEl2/Ys/2zbvym7Ze0eGbb6FrV8aueg89TPTNWAKlN
N49q6S3zLG1WmI2rVYz4LtPgpg1YR9FQRIg4Ll0C02daufXgvUBGjIARH19FTw6P
61kEhnEQxUHhdAqbAgMBAAE=
-----END PUBLIC KEY-----
		`,
		`-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCvBTz/TOYc66qB97OyYenSHk4T
hAUKX5RUWZ/80o0zyJoo1dfrrwW9PlT5o4DlGMs0NSbtJ8RMQRTLZwL/zxXjiEMv
dKFs2OrefYKANTc0e2XAtQAm3Is5Ro8AF1S4Fk+eZXr2yZtBRKXvhJ/A2bilVoSn
fmQnyBe7dVU43NXfrQIDAQAB
-----END PUBLIC KEY-----
		`,
	}

	keys := make([]crypto.PublicKey, 0, len(testData))

	for _, pubPEM := range testData {
		block, _ := pem.Decode([]byte(pubPEM))
		if block == nil {
			g.Fatal("failed to parse PEM block containing the public key")
			return // no-op because g.Fatal calls t.FailNow() but `staticcheck` doesn't know it
		}

		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			g.Fatalf("failed to parse DER encoded public key: %v", err)
		}

		keys = append(keys, pub)
	}

	l := launcher.New()

	err := l.IgnoreCerts(keys)
	if err != nil {
		g.Fatalf("IgnoreCerts: %s", err)
	}

	expected := "--ignore-certificate-errors-spki-list=" + strings.Join([]string{
		"+ZqfrXb+V/36nZecO59bghHlNhiHTzImjYLnNWGUd1I=",
		"llpTCSqZ2/IKsMg4tz+o1mCkXIOdKcM6sKu9kC6o7S4=",
	}, ",")

	g.Has(l.FormatArgs(), expected)
}

func TestIgnoreCerts_InvalidCert(t *testing.T) {
	g := setup(t)

	l := launcher.New()

	err := l.IgnoreCerts([]crypto.PublicKey{nil})
	if err == nil {
		g.Fatalf("IgnoreCerts: %s", err)
	}
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
