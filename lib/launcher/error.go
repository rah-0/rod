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
	"launcher manager requires HTTPS or WSS for non-loopback connections",
)
