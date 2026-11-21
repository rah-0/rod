package launcher

import "errors"

// ErrAlreadyLaunched is an error that indicates the launcher has already been launched.
var ErrAlreadyLaunched = errors.New("already launched")

// ErrBrowserNotFound indicates that no installed browser executable could be found.
var ErrBrowserNotFound = errors.New(
	"browser executable not found; install Chrome, Chromium, or Edge, or configure Launcher.Bin",
)

// ErrManagerUnauthorized indicates that a launcher manager rejected its bearer credential.
var ErrManagerUnauthorized = errors.New("launcher manager authentication failed")

// ErrManagerInsecureTransport indicates that a remote manager URL would expose its bearer credential.
var ErrManagerInsecureTransport = errors.New(
	"launcher manager requires authenticated HTTPS or WSS, including loopback",
)

// ErrManagedLaunch indicates that Launch, MustLaunch, or LaunchNew was called
// on a launcher from NewManaged, whose settings are meant for the manager's host.
var ErrManagedLaunch = errors.New("owned launch requires a local launcher")

// ErrDebuggingPortInUse indicates that the configured local port cannot be bound.
var ErrDebuggingPortInUse = errors.New("browser debugging port is unavailable")

// ErrDevToolsUnavailable indicates a process that exited before announcing DevTools.
var ErrDevToolsUnavailable = errors.New("browser exited before announcing its DevTools endpoint")

// ErrInvalidURL indicates a malformed URL or a value Chromium could parse as a switch.
var ErrInvalidURL = errors.New("launcher requires an absolute URL with a supported scheme")

// ErrSecureExecution indicates an attempted launch from an elevated Unix invocation.
var ErrSecureExecution = errors.New("launcher refuses secure-execution credential transitions; run browser automation without setuid, setgid, or file capabilities")
