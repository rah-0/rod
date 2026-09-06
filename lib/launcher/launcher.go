// Package launcher for launching browser utils.
package launcher

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/launcher/flags"
	"github.com/rah-0/rod/lib/utils"
)

// DefaultUserDataDirPrefix ...
var DefaultUserDataDirPrefix = filepath.Join(os.TempDir(), "rod", "user-data")

// Launcher is a helper to launch browser binary smartly.
type Launcher struct {
	Flags map[flags.Flag][]string `json:"flags"`

	ctx       context.Context
	ctxCancel func()

	logger io.Writer
	output outputTail

	parser             *URLParser
	pid                int
	exit               chan struct{}
	processStop        func()
	cleanupUserDataDir string
	cleanupErr         error

	profileRoot      *os.Root
	managedProfile   *managedProfile
	ownedUserDataDir string

	managed      bool
	serviceURL   string
	managerToken string

	isLaunched atomic.Bool
}

// New returns the default arguments to start browser.
// Headless will be enabled by default.
// UserDataDir uses a temporary directory by default, removed when the browser exits.
// If [Launcher.Bin] is empty, it searches for an installed Chrome, Chromium, or Edge browser.
func New() *Launcher {
	defaults.Load()

	dir := defaults.Dir
	ownedUserDataDir := ""
	if dir == "" {
		dir = filepath.Join(DefaultUserDataDirPrefix, utils.RandString(8))
		ownedUserDataDir = dir
	}

	defaultFlags := map[flags.Flag][]string{
		flags.Bin: {defaults.Bin},

		flags.UserDataDir: {dir},

		// use random port by default
		flags.RemoteDebuggingPort: {defaults.Port},

		// enable headless by default
		flags.Headless: nil,

		// to disable the init blank window
		"no-first-run":      nil,
		"no-startup-window": nil,

		// TODO: about the "site-per-process" see https://github.com/puppeteer/puppeteer/issues/2548
		"disable-features": {"site-per-process", "TranslateUI"},

		"disable-dev-shm-usage":                              nil,
		"disable-background-networking":                      nil,
		"disable-background-timer-throttling":                nil,
		"disable-backgrounding-occluded-windows":             nil,
		"disable-breakpad":                                   nil,
		"disable-client-side-phishing-detection":             nil,
		"disable-component-extensions-with-background-pages": nil,
		"disable-default-apps":                               nil,
		"disable-hang-monitor":                               nil,
		"disable-ipc-flooding-protection":                    nil,
		"disable-popup-blocking":                             nil,
		"disable-prompt-on-repost":                           nil,
		"disable-renderer-backgrounding":                     nil,
		"disable-sync":                                       nil,
		"disable-site-isolation-trials":                      nil,
		"enable-automation":                                  nil,
		"enable-features":                                    {"NetworkService", "NetworkServiceInProcess"},
		"force-color-profile":                                {"srgb"},
		"metrics-recording-only":                             nil,
		"use-mock-keychain":                                  nil,
	}

	if defaults.Show {
		delete(defaultFlags, flags.Headless)
	}
	if defaults.Devtools {
		defaultFlags["auto-open-devtools-for-tabs"] = nil
	}
	if inContainer {
		defaultFlags[flags.NoSandbox] = nil
	}
	if defaults.Proxy != "" {
		defaultFlags[flags.ProxyServer] = []string{defaults.Proxy}
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Launcher{
		ctx:              ctx,
		ctxCancel:        cancel,
		Flags:            defaultFlags,
		exit:             make(chan struct{}),
		parser:           NewURLParser().Context(ctx),
		logger:           io.Discard,
		output:           outputTail{limit: maxBrowserOutput},
		ownedUserDataDir: ownedUserDataDir,
	}
}

// NewUserMode is a preset to enable reusing current user data. Useful for automation of personal browser.
// If you see any error, it may because you can't launch debug port for existing browser, the solution is to
// completely close the running browser. Unfortunately, there's no API for rod to tell it automatically yet.
func NewUserMode() *Launcher {
	ctx, cancel := context.WithCancel(context.Background())
	bin, _ := LookPath()

	return &Launcher{
		ctx:       ctx,
		ctxCancel: cancel,
		Flags: map[flags.Flag][]string{
			flags.RemoteDebuggingPort: {"37712"},
			"no-startup-window":       nil,
			flags.Bin:                 {bin},
		},
		exit:   make(chan struct{}),
		parser: NewURLParser().Context(ctx),
		logger: io.Discard,
		output: outputTail{limit: maxBrowserOutput},
	}
}

// NewAppMode is a preset to run the browser like a native application.
// The u should be a URL.
func NewAppMode(u string) *Launcher {
	l := New()
	l.Set(flags.App, u).
		Set(flags.Env, "GOOGLE_API_KEY=no").
		Headless(false).
		Delete("no-startup-window").
		Delete("enable-automation")
	return l
}

// Context sets the context.
func (l *Launcher) Context(ctx context.Context) *Launcher {
	ctx, cancel := context.WithCancel(ctx)
	l.ctx = ctx
	l.parser.Context(ctx)
	l.ctxCancel = cancel
	return l
}

// Set a command line argument when launching the browser.
// Be careful the first argument is a flag name, it shouldn't contain values. The values the will be joined with comma.
// A flag can have multiple values. If no values are provided the flag will be a boolean flag.
// You can use the [Launcher.FormatArgs] to debug the final CLI arguments.
// List of available flags: https://peter.sh/experiments/chromium-command-line-switches
func (l *Launcher) Set(name flags.Flag, values ...string) *Launcher {
	name.Check()
	if name.NormalizeFlag() == flags.UserDataDir {
		l.ownedUserDataDir = ""
	}
	l.Flags[name.NormalizeFlag()] = values
	return l
}

// Get flag's first value.
func (l *Launcher) Get(name flags.Flag) string {
	if list, has := l.GetFlags(name); has {
		return list[0]
	}
	return ""
}

// Has flag or not.
func (l *Launcher) Has(name flags.Flag) bool {
	_, has := l.GetFlags(name)
	return has
}

// GetFlags from settings.
func (l *Launcher) GetFlags(name flags.Flag) ([]string, bool) {
	flag, has := l.Flags[name.NormalizeFlag()]
	return flag, has
}

// Append values to the flag.
func (l *Launcher) Append(name flags.Flag, values ...string) *Launcher {
	flags, has := l.GetFlags(name)
	if !has {
		flags = []string{}
	}
	return l.Set(name, append(flags, values...)...)
}

// Delete a flag.
func (l *Launcher) Delete(name flags.Flag) *Launcher {
	delete(l.Flags, name.NormalizeFlag())
	return l
}

// Bin sets the browser executable path. If empty, Launcher searches for an installed browser.
func (l *Launcher) Bin(path string) *Launcher {
	return l.Set(flags.Bin, path)
}

// Headless switch. Whether to run browser in headless mode. A mode without visible UI.
func (l *Launcher) Headless(enable bool) *Launcher {
	if enable {
		return l.Set(flags.Headless)
	}
	return l.Delete(flags.Headless)
}

// HeadlessNew switch is the "--headless=new" switch: https://developer.chrome.com/docs/chromium/new-headless
func (l *Launcher) HeadlessNew(enable bool) *Launcher {
	if enable {
		return l.Set(flags.Headless, "new")
	}
	return l.Delete(flags.Headless)
}

// NoSandbox switch. Whether to run browser in no-sandbox mode.
// Linux users may face "running as root without --no-sandbox is not supported" in some Linux/Chrome combinations.
// This function helps switch mode easily.
// Be aware disabling sandbox is not trivial. Use at your own risk.
// Related doc: https://bugs.chromium.org/p/chromium/issues/detail?id=638180
func (l *Launcher) NoSandbox(enable bool) *Launcher {
	if enable {
		return l.Set(flags.NoSandbox)
	}
	return l.Delete(flags.NoSandbox)
}

// XVFB enables a virtual display for a trusted local browser launch on Linux.
// [Manager] rejects remote XVFB configuration.
func (l *Launcher) XVFB(args ...string) *Launcher {
	return l.Set(flags.XVFB, args...)
}

// Preferences set chromium user preferences, such as set the default search engine or disable the pdf viewer.
// The pref is a json string, the doc is here
// https://src.chromium.org/viewvc/chrome/trunk/src/chrome/common/pref_names.cc
func (l *Launcher) Preferences(pref string) *Launcher {
	return l.Set(flags.Preferences, pref)
}

// AlwaysOpenPDFExternally switch.
// It will set chromium user preferences to enable the always_open_pdf_externally option.
func (l *Launcher) AlwaysOpenPDFExternally() *Launcher {
	return l.Set(flags.Preferences, `{"plugins":{"always_open_pdf_externally": true}}`)
}

// Devtools switch to auto open devtools for each tab.
func (l *Launcher) Devtools(autoOpenForTabs bool) *Launcher {
	if autoOpenForTabs {
		return l.Set("auto-open-devtools-for-tabs")
	}
	return l.Delete("auto-open-devtools-for-tabs")
}

// IgnoreCerts configure the Chrome's ignore-certificate-errors-spki-list argument with the public keys.
func (l *Launcher) IgnoreCerts(pks []crypto.PublicKey) error {
	spkis := make([]string, 0, len(pks))

	for _, pk := range pks {
		spki, err := certSPKI(pk)
		if err != nil {
			return fmt.Errorf("certSPKI: %w", err)
		}
		spkis = append(spkis, string(spki))
	}

	l.Set("ignore-certificate-errors-spki-list", spkis...)

	return nil
}

// UserDataDir is where the browser will look for all of its state, such as cookie and cache.
// When set to empty, browser will use current OS home dir.
// Related doc: https://chromium.googlesource.com/chromium/src/+/master/docs/user_data_dir.md
func (l *Launcher) UserDataDir(dir string) *Launcher {
	if dir == "" {
		l.Delete(flags.UserDataDir)
	} else {
		l.Set(flags.UserDataDir, dir)
	}
	return l
}

// ProfileDir is the browser profile the browser will use.
// When set to empty, the profile 'Default' is used.
// Related article: https://superuser.com/a/377195
func (l *Launcher) ProfileDir(dir string) *Launcher {
	if dir == "" {
		l.Delete(flags.ProfileDir)
	} else {
		l.Set(flags.ProfileDir, dir)
	}
	return l
}

// RemoteDebuggingPort to launch the browser. Zero for a random port. Zero is the default value.
// If it is not zero, the launcher tries to reconnect to that port first. If
// reconnection fails, it launches a new browser.
func (l *Launcher) RemoteDebuggingPort(port int) *Launcher {
	return l.Set(flags.RemoteDebuggingPort, fmt.Sprintf("%d", port))
}

// Proxy for the browser.
func (l *Launcher) Proxy(host string) *Launcher {
	return l.Set(flags.ProxyServer, host)
}

// WindowSize for the browser.
func (l *Launcher) WindowSize(x, y int) *Launcher {
	return l.Set(flags.WindowSize, fmt.Sprintf("%d,%d", x, y))
}

// WindowPosition for the browser.
func (l *Launcher) WindowPosition(x, y int) *Launcher {
	return l.Set(flags.WindowPosition, fmt.Sprintf("%d,%d", x, y))
}

// WorkingDir to launch the browser process. [Manager] keeps it server-owned.
func (l *Launcher) WorkingDir(path string) *Launcher {
	return l.Set(flags.WorkingDir, path)
}

// Env to launch the browser process. [Manager] keeps it server-owned.
// The default value is [os.Environ]().
// Usually you use it to set the timezone env. Such as:
//
//	Env(append(os.Environ(), "TZ=Asia/Tokyo")...)
func (l *Launcher) Env(env ...string) *Launcher {
	return l.Set(flags.Env, env...)
}

// StartURL to launch.
func (l *Launcher) StartURL(u string) *Launcher {
	return l.Set("", u)
}

// FormatArgs returns the formatted CLI arguments without modifying launch options.
func (l *Launcher) FormatArgs() []string {
	execArgs := []string{}
	for k, v := range l.Flags {
		if k == flags.Arguments {
			continue
		}

		if strings.HasPrefix(string(k), "rod-") {
			continue
		}

		// fix a bug of chrome, if path is not absolute chrome will hang
		if k == flags.UserDataDir {
			abs, err := filepath.Abs(v[0])
			utils.E(err)
			v = append([]string{abs}, v[1:]...)
		}

		str := "--" + string(k)
		if v != nil {
			str += "=" + strings.Join(v, ",")
		}
		execArgs = append(execArgs, str)
	}

	execArgs = append(execArgs, l.Flags[flags.Arguments]...)
	slices.Sort(execArgs)
	return execArgs
}

// Logger to handle stdout and stderr from browser.
// For example, pipe all browser output to stdout:
//
//	launcher.New().Logger(os.Stdout)
func (l *Launcher) Logger(w io.Writer) *Launcher {
	l.logger = w
	return l
}

// MustLaunch is similar to Launch.
func (l *Launcher) MustLaunch() string {
	u, err := l.Launch()
	utils.E(err)
	return u
}

// Launch a standalone temp browser instance and returns the debug url.
// bin and profileDir are optional, set them to empty to use the default values.
// If you want to reuse sessions, such as cookies, set the [Launcher.UserDataDir] to the same location.
//
// Please note launcher can only be used once.
func (l *Launcher) Launch() (string, error) {
	return l.launch(nil, true)
}

// LaunchNew starts a new local process, never attaching to an existing debugging
// port. Both ctx and the launcher context can cancel startup. Managed launchers
// are rejected. Like Launch, a launcher can be used only once.
func (l *Launcher) LaunchNew(ctx context.Context) (string, error) {
	if l.managed {
		return "", ErrManagedLaunch
	}
	return l.launch(ctx, false)
}

func (l *Launcher) launch(ctx context.Context, reuse bool) (u string, err error) {
	if l.hasLaunched() {
		return "", ErrAlreadyLaunched
	}

	defer l.ctxCancel()
	if ctx != nil {
		startup, cancel := context.WithCancel(ctx)
		if l.ctx.Err() != nil {
			cancel()
		}
		stop := context.AfterFunc(l.ctx, cancel)
		defer stop()
		defer cancel()
		l.ctx = startup
		l.parser.Context(startup)
	}
	started := false
	defer func() {
		if !started {
			l.cleanupErr = l.removeUserDataDir()
			err = errors.Join(err, l.cleanupErr)
			close(l.exit)
		}
	}()
	if err := l.ctx.Err(); err != nil {
		return "", err
	}

	bin, err := l.getBin()
	if err != nil {
		return "", err
	}

	port := l.Get(flags.RemoteDebuggingPort)
	if port != "" && port != "0" {
		if reuse {
			u, err := ResolveURL(l.ctx, port)
			if err == nil {
				return u, nil
			}
		} else {
			listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
			if err != nil {
				return "", fmt.Errorf("%w: %s: %w", ErrDebuggingPortInUse, port, err)
			}
			_ = listener.Close()
		}
	}
	// Snapshot the owned path before starting the process. Never remove a
	// directory selected later by a caller changing launch options.
	if dir := l.Get(flags.UserDataDir); dir != "" && dir == l.ownedUserDataDir {
		profile, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(profile), 0o700); err != nil {
			return "", err
		}
		// A pre-existing path belongs to somebody else, even if it happens to
		// match the random default. Establish ownership only after creation.
		if err := os.Mkdir(profile, 0o700); err != nil {
			return "", err
		}
		l.cleanupUserDataDir = profile
	}
	if l.cleanupUserDataDir == "" {
		if err := l.setupUserPreferences(); err != nil {
			return "", err
		}
	}
	args := l.FormatArgs()
	cmd := exec.Command(bin, args...)

	l.setupCmd(cmd)

	l.pid, l.processStop, err = l.startProcess(cmd)
	if err != nil && cmd.Process == nil {
		return "", err
	}
	started = true

	go func() {
		_ = cmd.Wait()
		l.processStop()
		l.cleanupErr = l.removeUserDataDir()
		close(l.exit)
	}()

	if err == nil {
		u, err = l.getURL()
		if err != nil && !reuse && l.ctx.Err() == nil && l.exited() {
			// The owned API reports output through its explicitly bounded tail.
			// Avoid embedding the parser's separate startup buffer as well.
			err = ErrDevToolsUnavailable
		}
		if err == nil {
			u, err = ResolveURL(l.ctx, u)
		}
	}
	if err != nil {
		l.Kill()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, l.CleanupContext(cleanup))
	}
	return u, err
}

func (l *Launcher) hasLaunched() bool {
	return !l.isLaunched.CompareAndSwap(false, true)
}

func (l *Launcher) setupUserPreferences() error {
	userDir := l.Get(flags.UserDataDir)
	pref := l.Get(flags.Preferences)
	if userDir == "" || pref == "" {
		return nil
	}
	userDir, err := filepath.Abs(userDir)
	if err != nil {
		return err
	}
	profile := l.Get(flags.ProfileDir)
	if profile == "" {
		profile = "Default"
	}
	if l.profileRoot != nil {
		if err := l.profileRoot.MkdirAll(profile, 0o700); err != nil {
			return err
		}
		return l.profileRoot.WriteFile(filepath.Join(profile, "Preferences"), []byte(pref), 0o600)
	}
	path := filepath.Join(userDir, profile, "Preferences")
	return utils.OutputFile(path, pref)
}

func (l *Launcher) setupCmd(cmd *exec.Cmd) {
	l.osSetupCmd(cmd)

	dir := l.Get(flags.WorkingDir)
	env, _ := l.GetFlags(flags.Env)
	cmd.Dir = dir
	cmd.Env = env

	// One writer also serializes the configured logger across both streams.
	output := io.MultiWriter(&l.output, l.logger, l.parser)
	cmd.Stdout = output
	cmd.Stderr = output
	// A descendant retaining an output pipe must not keep process cleanup stuck.
	cmd.WaitDelay = 5 * time.Second
}

func (l *Launcher) getBin() (string, error) {
	return resolveBin(l.Get(flags.Bin), LookPath)
}

func resolveBin(configured string, search func() (string, bool)) (string, error) {
	if configured != "" {
		return configured, nil
	}

	if bin, found := search(); found {
		return bin, nil
	}

	return "", ErrBrowserNotFound
}

func (l *Launcher) getURL() (u string, err error) {
	select {
	case <-l.ctx.Done():
		err = l.ctx.Err()
	case u = <-l.parser.URL:
	case err = <-l.parser.errors:
	case <-l.exit:
		select {
		case err = <-l.parser.errors:
		default:
			err = l.parser.Err()
		}
	}
	return
}

// PID returns the browser process pid.
func (l *Launcher) PID() int {
	return l.pid
}

// Kill requests termination of the browser process owned by this launcher.
// Use Cleanup to wait for its exit and temporary profile removal.
func (l *Launcher) Kill() {
	if l.processStop == nil || l.exited() {
		return
	}
	l.processStop()
}

func (l *Launcher) exited() bool {
	select {
	case <-l.exit:
		return true
	default:
		return false
	}
}

// Cleanup waits until the browser exits and removes only the temporary user
// data directory generated by New. It is safe before Launch, after a failed
// launch, and when reusing an existing browser. An explicitly configured
// directory remains owned by the caller. Cleanup does not stop a live browser;
// call Kill first when it should be terminated.
func (l *Launcher) Cleanup() {
	if !l.isLaunched.Load() {
		return
	}
	<-l.exit
}

// CleanupContext waits for process exit and owned profile removal until ctx ends.
// It does not start a waiter goroutine or stop a live process; call Kill first.
// A timeout leaves ownership with the launcher, so cleanup can be checked again.
func (l *Launcher) CleanupContext(ctx context.Context) error {
	if !l.isLaunched.Load() {
		return nil
	}
	select {
	case <-l.exit:
		return l.cleanupErr
	default:
	}
	select {
	case <-l.exit:
		return l.cleanupErr
	case <-ctx.Done():
		return fmt.Errorf("wait for browser process %d and profile cleanup: %w", l.pid, ctx.Err())
	}
}

// Done closes when the launch attempt and its owned process/profile cleanup end.
// Before a launch attempt it remains open.
func (l *Launcher) Done() <-chan struct{} { return l.exit }

func (l *Launcher) removeUserDataDir() error {
	if l.cleanupUserDataDir != "" {
		if err := os.RemoveAll(l.cleanupUserDataDir); err != nil {
			return fmt.Errorf("remove browser profile %q: %w", l.cleanupUserDataDir, err)
		}
	}
	return nil
}

// OutputTail configures how many recent stdout/stderr bytes Output retains.
// The default is 64 KiB. Zero disables capture; a negative limit panics.
// Configure it before launch. Logger continues to receive the complete output.
func (l *Launcher) OutputTail(limit int) *Launcher {
	if limit < 0 {
		panic("launcher: negative output limit")
	}
	l.output.limit = limit
	return l
}

// Output returns a concurrency-safe snapshot of recent stdout/stderr, including
// output written after the DevTools endpoint was announced.
func (l *Launcher) Output() string {
	l.output.mu.Lock()
	defer l.output.mu.Unlock()
	return string(l.output.data)
}

type outputTail struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (tail *outputTail) Write(data []byte) (int, error) {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	n := len(data)
	if tail.limit == 0 {
		return n, nil
	}
	if len(data) >= tail.limit {
		tail.data = append(tail.data[:0], data[len(data)-tail.limit:]...)
	} else {
		if excess := len(tail.data) + len(data) - tail.limit; excess > 0 {
			copy(tail.data, tail.data[excess:])
			tail.data = tail.data[:len(tail.data)-excess]
		}
		tail.data = append(tail.data, data...)
	}
	return n, nil
}
