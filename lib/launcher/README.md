# Overview

See the [runnable example](../../examples/owned-launch/main.go).

A lib that helps to find and launch a locally installed browser. You can also use it as a standalone lib without Rod.

Rod doesn't download browser binaries. Install Chrome, Chromium, or Edge yourself, or provide an explicit executable path with `Launcher.Bin`.

See the [platform support matrix](../../doc/PLATFORM_SUPPORT.md) for exact tested
environments, compilation-only evidence, and platform cleanup limitations.

Use `Browser.Launch` to connect and own a configured local launch:

```go
l := launcher.New().Bin("/usr/bin/chromium").Headless(true).OutputTail(64 * 1024)
browser := rod.New()
if err := browser.Launch(l); err != nil {
    return err
}
defer browser.Close()
```

`Browser.Launch` rejects an already-used launcher, a remote managed launcher,
`ControlURL`, a configured `Client`, or an incognito browser. It always starts a
new process: an occupied debugging port returns an error and is never adopted.
Launcher options apply only to that launch. Startup and connection errors wrap
their original causes and include the configured recent output tail.

`Browser.CloseWithTimeout(5 * time.Second)` provides a total cleanup budget
independent of navigation deadlines. It allows up to half that budget (at most
five seconds) for graceful close, then terminates the owned process and waits
for exit and temporary profile removal. `Browser.Close` uses a ten-second total
budget. Cleanup errors, including timeouts and failed profile deletion, are
returned. A timeout retains ownership so cleanup can be checked again. Custom
CDP clients must honor request cancellation; interrupted built-in transport
writes close the connection because a partially written frame is unusable.

For standalone use, `Launcher.LaunchNew(ctx)` also rejects port reuse. The
existing `Launcher.Launch` retains its debugging-port attachment behavior.
Register `Kill` and `CleanupContext` before launching when managing ownership
yourself. `CleanupContext(ctx)` waits directly on the process completion signal
without creating a cleanup waiter; it returns when the budget expires, while
the launcher's process reaper remains responsible for eventual process exit.

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

Newly created launcher-generated temporary profiles are removed automatically
after process exit, including failed startup. A pre-existing path at the generated
location remains caller-owned. `Launcher.Cleanup` waits for that removal and is
safe to repeat, call before launch, or call after attaching to an existing
debugging port. It does not stop a live browser; use `Kill` first. A profile
supplied through `UserDataDir` remains caller-owned.

`Output` keeps the most recent 64 KiB of stdout/stderr, including output after
the DevTools endpoint appears. Configure the limit before launch with
`OutputTail(bytes)`; zero disables this capture. Reads and concurrent writes are
safe. `Logger` still receives the complete stream and must not block indefinitely.

Unix supervisors also give each browser a private scratch directory beneath its
configured `TMPDIR` parent (or `/tmp`). Only the browser's environment points to
that child directory. After its descendants exit, the supervisor removes the
child, including Chrome's separate socket directories. The caller's temporary
directory and existing files remain untouched.

An automatically launched `Browser` also stops its process when the context
used for `Connect` is canceled or its CDP connection ends. Later operation
context clones do not change process ownership. `Browser.Close` starts cleanup independently of an expired operation context
and uses the bounded graceful-close and process-cleanup budget above. Closing an incognito context leaves its parent
browser running. Passing a `ControlURL` does not transfer launcher ownership.

The remote manager owns each browser and its randomly named profile and cleans
them up when the WebSocket session ends. On Unix its supervisor also inherits
the profile's parent directory descriptor, so cleanup after manager death stays
inside the original directory even if a parent symlink is replaced. Reopening
that descriptor requires `/proc/self/fd` on Linux or `/dev/fd` on other Unix
systems; launch fails if the directory cannot be reopened safely.

## Persistent automation profiles

Chrome 136 and later ignore remote-debugging switches for the default Chrome
data directory. Closing an existing Chrome process does not remove this
restriction. Use an explicit non-default directory for automation, as described
in [Chrome's remote-debugging guidance](https://developer.chrome.com/blog/remote-debugging-port):

```go
l := launcher.New().UserDataDir("/path/to/rod-automation-profile")
```

The directory is persistent and caller-owned: `Cleanup`, `CleanupContext`, and
an owning `Browser.Close` preserve it. Reuse the same directory on later runs,
with only one browser using it at a time. Sign in to sites separately in this
automation profile; it does not inherit the personal profile's cookies or
sessions. Whether a login survives a restart also depends on the site's session
policy. Rod does not copy or recover a personal profile.

`NewUserMode` keeps its default-directory behavior for browsers that support it.
It does not detect browser versions or bypass Chrome's restriction. An explicit
`NewUserMode().UserDataDir(...)` also selects a caller-owned automation directory.
Use an installed browser; Rod does not download a replacement.

See the [persistent profile example](../../examples/persistent-profile/README.md)
for separate login setup and a runnable launch with bounded cleanup.

## Remote manager security

`rod-manager` requires a bearer token from `ROD_MANAGER_TOKEN` and listens on
`127.0.0.1:7317` by default. Pass the same token explicitly to
`NewManaged(ctx, serviceURL, token)`. Its context covers the initial HTTP request
and response decoding, then subsequent connection establishment. Supply a
deadline to bound initialization.
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

`LookPath` searches installed browser candidates for the current platform,
including `chrome` and `chromium` on FreeBSD. `FormatArgs` resolves relative
profile paths in the returned command arguments without changing launcher
configuration or ownership.

`ResolveURL(ctx, endpoint)` normalizes a browser endpoint and requests
`/json/version` with a 10-second timeout, including the response body. Its context
accepts caller cancellation or a shorter deadline. `Launcher.Launch` passes its
context to discovery. HTTP status, body-read, JSON, and WebSocket URL errors are
returned to the caller; `MustResolveURL(ctx, endpoint)` panics on errors.
