package testutil

import "errors"

// ErrMissingResponseBody indicates that an HTTP exchange returned no readable body.
var ErrMissingResponseBody = errors.New("missing HTTP response body")
