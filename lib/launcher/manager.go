package launcher

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/launcher/flags"
	"github.com/rah-0/rod/lib/utils"
)

const (
	// HeaderName for remote launch.
	HeaderName = "Rod-Launcher"

	// ManagerTokenEnv is the environment variable used by the rod-manager command.
	ManagerTokenEnv = "ROD_MANAGER_TOKEN"
)

// MustNewManaged is similar to NewManaged.
func MustNewManaged(serviceURL, authToken string) *Launcher {
	l, err := NewManaged(serviceURL, authToken)
	utils.E(err)
	return l
}

// NewManaged creates a default Launcher instance from launcher.Manager.
// The serviceURL must point to a launcher.Manager. It will send a http request to the serviceURL
// to get the default settings of the Launcher instance. For example if the launcher.Manager running on a
// Linux machine will return different default settings from the one on Mac.
// The manager kills the remote browser after the WebSocket is closed.
// The authToken is sent as a bearer credential on both the initial HTTP request and the WebSocket handshake.
// Plain HTTP and WebSocket URLs are accepted only for loopback hosts; use HTTPS or WSS remotely.
func NewManaged(serviceURL, authToken string) (*Launcher, error) {
	if serviceURL == "" {
		serviceURL = "ws://127.0.0.1:7317"
	}

	u, err := url.Parse(serviceURL)
	if err != nil {
		return nil, err
	}
	if managerURLUsesPlaintext(u) && !managerLoopbackHost(u.Hostname()) {
		return nil, ErrManagerInsecureTransport
	}

	l := New()
	l.managed = true
	l.managerToken = authToken
	l.serviceURL = toWS(*u).String()
	l.Flags = nil

	req, err := http.NewRequestWithContext(l.ctx, http.MethodGet, toHTTP(*u).String(), nil)
	if err != nil {
		return nil, err
	}
	l.addManagerAuthorization(req.Header)

	httpClient := *http.DefaultClient
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, ErrManagerUnauthorized
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("launcher manager returned HTTP status %d", res.StatusCode)
	}

	return l, json.NewDecoder(res.Body).Decode(l)
}

// JSON serialization.
func (l *Launcher) JSON() []byte {
	return utils.MustToJSONBytes(l)
}

// MustClient similar to Launcher.Client.
func (l *Launcher) MustClient() *cdp.Client {
	u, h := l.ClientHeader()
	return cdp.MustStartWithURL(l.ctx, u, h)
}

// Client for launching browser remotely via the launcher.Manager.
func (l *Launcher) Client() (*cdp.Client, error) {
	u, h := l.ClientHeader()
	return cdp.StartWithURL(l.ctx, u, h)
}

// ClientHeader for launching browser remotely via the launcher.Manager.
func (l *Launcher) ClientHeader() (string, http.Header) {
	l.mustManaged()
	header := http.Header{}
	header.Set(HeaderName, utils.MustToJSON(l))
	l.addManagerAuthorization(header)
	return l.serviceURL, header
}

func (l *Launcher) addManagerAuthorization(header http.Header) {
	if l.managerToken != "" {
		header.Set("Authorization", "Bearer "+l.managerToken)
	}
}

func (l *Launcher) mustManaged() {
	if !l.managed {
		panic("Must be used with launcher.NewManaged")
	}
}

var _ http.Handler = &Manager{}

// Manager is used to launch browsers via http server on another machine.
// The reason why we have Manager is after we launcher a browser, we can't dynamically change its
// CLI arguments, such as "--headless". The Manager allows us to decide what CLI arguments to
// pass to the browser when launch it remotely.
// The work flow looks like:
//
//	|                 Machine X                  |                      Machine Y                      |
//	| NewManaged("wss://manager", authToken) -|-> TLS endpoint -> launcher.NewManager(authToken) |
//
//	1. X send a http request to Y, Y respond default Launcher settings based the OS of Y.
//	2. X start a websocket connect to Y with the Launcher settings
//	3. Y launches a browser with the Launcher settings X
//	4. Y transparently proxy the websocket connect between X and the launched browser
type Manager struct {
	// Logger for key events
	Logger utils.Logger

	// Defaults should return the default Launcher settings
	Defaults func(http.ResponseWriter, *http.Request) *Launcher

	// BeforeLaunch hook is called right before the launching with the Launcher instance that will be used
	// to launch the browser. Client input is validated before this trusted server hook. The hook may
	// change process settings, but the manager reasserts its profile and debugging port afterward.
	BeforeLaunch func(*Launcher, http.ResponseWriter, *http.Request)

	authTokenHash [sha256.Size]byte
	hasAuthToken  bool
	allowedBin    string
	userDataRoot  string
}

// NewManager returns an authenticated remote browser launcher.
// An empty authToken locks the manager: every request is rejected.
func NewManager(authToken string) *Manager {
	defaults.Load()

	tokenHash := sha256.Sum256([]byte(authToken))
	userDataRoot, err := filepath.EvalSymlinks(os.TempDir())
	utils.E(err)
	userDataRoot, err = filepath.Abs(userDataRoot)
	utils.E(err)

	return &Manager{
		Logger:        utils.LoggerQuiet,
		Defaults:      func(_ http.ResponseWriter, _ *http.Request) *Launcher { return New() },
		authTokenHash: tokenHash,
		hasAuthToken:  authToken != "",
		allowedBin:    defaults.Bin,
		userDataRoot:  userDataRoot,
		BeforeLaunch:  func(_ *Launcher, _ http.ResponseWriter, _ *http.Request) {},
	}
}

func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !m.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "[rod-manager] unauthorized", http.StatusUnauthorized)
		return
	}

	// Credentials belong to the manager and must never reach hooks or Chromium.
	r.Header.Del("Authorization")

	if r.Header.Get("Upgrade") == "websocket" {
		m.launch(w, r)
		return
	}

	l := m.Defaults(w, r)
	// The manager, not its clients, owns filesystem paths and listening ports.
	l.Delete(flags.UserDataDir).RemoteDebuggingPort(0)
	utils.E(w.Write(l.JSON()))
}

func (m *Manager) launch(w http.ResponseWriter, r *http.Request) {
	l := New().Context(r.Context())

	options := r.Header.Get(HeaderName)
	r.Header.Del(HeaderName)
	if options != "" {
		l.Flags = nil
		if err := json.Unmarshal([]byte(options), l); err != nil {
			rejectManagerLaunch(w, "[rod-manager] invalid launch options")
		}
	}

	m.validateLaunchOptions(l, w)

	profile, err := m.newManagedProfile()
	if err != nil {
		rejectManagerLaunchStatus(w, http.StatusInternalServerError, "[rod-manager] create browser profile")
	}
	l.UserDataDir(profile.path).RemoteDebuggingPort(0)
	l.profileRoot = profile.root
	defer m.cleanup(l, profile)

	m.BeforeLaunch(l, w, r)
	l.Env(managerBrowserEnvironment(l)...)
	// Hooks can tighten policy, but the client-controlled profile and debugging
	// port must remain server-owned at the final process boundary.
	l.UserDataDir(profile.path).RemoteDebuggingPort(0)
	l.profileRoot = profile.root

	u := l.MustLaunch()

	parsedURL, err := url.Parse(u)
	utils.E(err)

	m.Logger.Println("Launch PID:", l.PID())
	defer m.Logger.Println("Close PID:", l.PID())

	parsedWS, err := url.Parse(u)
	utils.E(err)
	parsedURL.Path = parsedWS.Path

	httputil.NewSingleHostReverseProxy(toHTTP(*parsedURL)).ServeHTTP(w, r)
}

type managedProfile struct {
	parent *os.Root
	root   *os.Root
	name   string
	path   string
}

func (m *Manager) newManagedProfile() (*managedProfile, error) {
	if err := os.MkdirAll(m.userDataRoot, 0o700); err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(m.userDataRoot)
	if err != nil {
		return nil, err
	}

	for range 10 {
		name := "rod-manager-" + rand.Text()
		err = parent.Mkdir(name, 0o700)
		if err == nil {
			root, openErr := parent.OpenRoot(name)
			if openErr != nil {
				_ = parent.RemoveAll(name)
				_ = parent.Close()
				return nil, openErr
			}
			return &managedProfile{
				parent: parent,
				root:   root,
				name:   name,
				path:   filepath.Join(m.userDataRoot, name),
			}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			_ = parent.Close()
			return nil, err
		}
	}

	_ = parent.Close()
	return nil, err
}

func (m *Manager) cleanup(l *Launcher, profile *managedProfile) {
	l.Kill()
	if l.PID() != 0 {
		<-l.exit
		m.Logger.Println("Killed PID:", l.PID())
	}

	if profile != nil {
		_ = profile.root.Close()
		if err := profile.parent.RemoveAll(profile.name); err == nil {
			m.Logger.Println("Removed user data directory")
		} else {
			m.Logger.Println("Failed to remove user data directory")
		}
		_ = profile.parent.Close()
	}
}

func (m *Manager) authorized(r *http.Request) bool {
	if !m.hasAuthToken {
		return false
	}

	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return false
	}

	tokenHash := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(tokenHash[:], m.authTokenHash[:]) == 1
}

func managerBrowserEnvironment(l *Launcher) []string {
	env, configured := l.GetFlags(flags.Env)
	if !configured {
		env = os.Environ()
	}

	filtered := make([]string, 0, len(env))
	for _, value := range env {
		name, _, _ := strings.Cut(value, "=")
		if strings.EqualFold(name, ManagerTokenEnv) {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}

func (m *Manager) validateLaunchOptions(l *Launcher, w http.ResponseWriter) {
	bin, hasBin := l.GetFlags(flags.Bin)
	if !hasBin || len(bin) != 1 || bin[0] != m.allowedBin {
		rejectManagerLaunch(w, "[rod-manager] remote option is not allowed: "+string(flags.Bin))
	}

	for _, f := range []flags.Flag{flags.Preferences, flags.ProfileDir, flags.RemoteDebuggingPort} {
		if values, has := l.GetFlags(f); has && len(values) != 1 {
			rejectManagerLaunch(w, "[rod-manager] invalid launch options")
		}
	}

	if profile, has := l.GetFlags(flags.ProfileDir); has && profile[0] != "" {
		clean := filepath.Clean(profile[0])
		if filepath.IsAbs(profile[0]) || clean == "." || clean == ".." || filepath.Base(clean) != clean {
			rejectManagerLaunch(w, "[rod-manager] remote path is not allowed: "+string(flags.ProfileDir))
		}
	}

	for f, values := range l.Flags {
		if f == flags.Arguments {
			for _, value := range values {
				if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") {
					rejectManagerLaunch(w, "[rod-manager] command-line switches must use named launch options")
				}
			}
			continue
		}

		if f.NormalizeFlag() != f || strings.Contains(string(f), "=") {
			rejectManagerLaunch(w, "[rod-manager] invalid launch option name")
		}
		if restrictedManagerFlag(f) {
			rejectManagerLaunch(w, "[rod-manager] remote option is not allowed: "+string(f))
		}
	}
}

func managerURLUsesPlaintext(u *url.URL) bool {
	return strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "ws")
}

func managerLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if address, _, hasZone := strings.Cut(host, "%"); hasZone {
		host = address
	}
	return net.ParseIP(host).IsLoopback()
}

func restrictedManagerFlag(f flags.Flag) bool {
	switch f {
	case flags.Env,
		flags.WorkingDir,
		flags.XVFB,
		"browser-subprocess-path",
		"disable-extensions-except",
		"gpu-launcher",
		"gssapi-library-name",
		"load-component-extension",
		"load-extension",
		"nacl-loader-cmd-prefix",
		"plugin-launcher",
		"ppapi-plugin-launcher",
		"register-pepper-plugins",
		"renderer-cmd-prefix",
		"utility-cmd-prefix",
		"zygote-cmd-prefix":
		return true
	default:
		return false
	}
}

func rejectManagerLaunch(w http.ResponseWriter, message string) {
	rejectManagerLaunchStatus(w, http.StatusBadRequest, message)
}

func rejectManagerLaunchStatus(w http.ResponseWriter, status int, message string) {
	http.Error(w, message, status)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	panic(http.ErrAbortHandler)
}
