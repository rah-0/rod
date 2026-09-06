# Overview

A lib that helps to find and launch a locally installed browser. You can also use it as a standalone lib without Rod.

Rod doesn't download browser binaries. Install Chrome, Chromium, or Edge yourself, or provide an explicit executable path with `Launcher.Bin`.

See the [platform support matrix](../../doc/PLATFORM_SUPPORT.md) for exact tested
environments, compilation-only evidence, and platform cleanup limitations.

Call `Browser.Close` for a browser launched automatically by Rod. For an explicit
launcher, register cleanup before starting it:

```go
l := launcher.New()
defer func() {
    l.Kill()
    l.Cleanup()
}()
controlURL := l.MustLaunch()
browser := rod.New().ControlURL(controlURL).MustConnect()
defer func() {
    browser.Timeout(5 * time.Second).MustClose()
}()
```

On Unix, each local launch runs under a supervisor. A private pipe ties it to the
launching application: application exit, panic, test timeout, or `SIGKILL` closes
the pipe and triggers browser cleanup. On Linux the supervisor also adopts,
terminates, and reaps surviving descendants, including detached Chrome crash
reporters. It requires access to `/proc`. Other Unix systems target the browser
process group; descendants that leave that group have weaker cleanup guarantees.
Windows uses direct launches and does not provide this application-crash cleanup.
Killing the supervisor itself or a host failure cannot guarantee profile cleanup.

The supervisor re-executes the application's binary with a private invocation
and inherited pipes. Package initializers that run before the launcher's
initializer run again in that helper; keep such initializers free of external
side effects. A helper that cannot initialize causes launch to fail. No helper
binary downloads or additional Go dependencies are needed. Starting Chrome
yourself with `exec.Command` bypasses this supervision.

Launcher-generated temporary profiles are removed automatically after process
exit, including failed startup. `Launcher.Cleanup` waits for that removal and is
safe to repeat, call before launch, or call after attaching to an existing
debugging port. It does not stop a live browser; use `Kill` first. A profile
supplied through `UserDataDir` remains caller-owned. Startup output capture keeps
only the most recent 64 KiB until the DevTools endpoint is found; `Logger` still
receives the complete stream and must not block indefinitely.

Unix supervisors also give each browser a private scratch directory beneath its
configured `TMPDIR` parent (or `/tmp`). Only the browser's environment points to
that child directory. After its descendants exit, the supervisor removes the
child, including Chrome's separate socket directories. The caller's temporary
directory and existing files remain untouched.

An automatically launched `Browser` also stops its process when the context
used for `Connect` is canceled or its CDP connection ends. Later operation
context clones do not change process ownership. `Browser.Close` allows five
seconds for graceful shutdown independently of an expired operation context,
then forces process cleanup. Closing an incognito context leaves its parent
browser running. Passing a `ControlURL` does not transfer launcher ownership.

The remote manager owns each browser and its randomly named profile and cleans
them up when the WebSocket session ends. On Unix its supervisor also inherits
the profile's parent directory descriptor, so cleanup after manager death stays
inside the original directory even if a parent symlink is replaced. Reopening
that descriptor requires `/proc/self/fd` on Linux or `/dev/fd` on other Unix
systems; launch fails if the directory cannot be reopened safely.

## Remote manager security

`rod-manager` requires a bearer token from `ROD_MANAGER_TOKEN` and listens on
`127.0.0.1:7317` by default. Pass the same token explicitly to `NewManaged`.
The command removes the token from its process environment after reading it,
and the manager strips that variable from every browser child environment. The
library-level `NewManager` is also locked when given an empty token.

Use HTTPS/WSS, a trusted TLS reverse proxy, or an encrypted tunnel before
binding the manager to a network interface. A bearer token sent over plain
HTTP/WebSocket can be intercepted. Managed clients reject plaintext
non-loopback URLs, and the command requires an explicit
`-allow-plaintext-remote` override for non-loopback listeners.

Treat every token holder as an administrator of the browser host. Managed
clients have full Chrome DevTools access. Process-owned launch settings,
including the browser executable, environment, working directory, and XVFB
wrapper, cannot be changed remotely. `Launcher.XVFB` remains available to
trusted local launchers. Managed clients also cannot choose the profile path or
debugging port; cleanup is confined to the manager-created profile.

## Browser discovery

`ResolveURL(ctx, endpoint)` normalizes a browser endpoint and requests
`/json/version` with a 10-second timeout, including the response body. Its context
accepts caller cancellation or a shorter deadline. `Launcher.Launch` passes its
context to discovery. HTTP status, body-read, JSON, and WebSocket URL errors are
returned to the caller; `MustResolveURL(ctx, endpoint)` panics on errors.
