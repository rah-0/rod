// Package guardian implements the cleanup supervisor that the launcher starts
// for every local browser on Linux by re-executing the current executable.
// Other systems do not enter private mode.
//
// This package's initializer answers the private invocation and never returns,
// so the supervisor runs only the package initializers that Go schedules
// before this one. Go initializes a package after the packages it imports and,
// among the packages that are ready, picks the one whose import path sorts
// first. This package therefore imports only os, os/signal, and packages that
// need nothing beyond what os needs. With Go 1.27 its initializer runs before
// that of unicode, a package without imports whose path sorts after nearly
// every other one, and the supervisor skips unicode and whatever depends on
// it: strings, bytes, reflect, fmt, and with them most of the launcher's
// dependencies and of a typical application. A package whose imports are all
// initialized by then, such as one that imports only os and sync, can still
// run its initializer in the supervisor.
//
// One more import can undo this. A package that depends on unicode, such as
// strings, encoding/json, os/exec or crypto/rand, is initialized after every
// ready package that sorts before unicode, and all of them would then run in
// the supervisor of every browser launch. That is why this package has its own
// message encoding, starts the browser with os.StartProcess and receives
// random names from the launcher. TestImports guards the import list.
package guardian
