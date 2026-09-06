package rod

import "errors"

// ErrLaunchConflict indicates that configured local launch conflicts with the browser configuration.
var ErrLaunchConflict = errors.New("owned launch requires an unconnected browser, a non-nil local launcher, and no ControlURL, Client, or incognito context")

// ErrCleanupTimeout indicates an invalid cleanup time budget.
var ErrCleanupTimeout = errors.New("browser cleanup timeout must be positive")
