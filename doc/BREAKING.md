# Breaking changes

Incompatible APIs, behavior, and development workflows are recorded here, newest
version first. Each entry compares that version with its previous release and
names the affected functions, types, fields, or commands with their migration.
When skipping releases, apply every intervening entry, oldest to newest.

Add pending changes under the planned next version, above all released versions,
and mark its status as planned. Each heading names the target version and the
previous release it changes. At release time, replace the planned status with
the release date. Keep released entries as history; add later changes under
their own planned version without moving older changes into it.

## v0.118.0 — compared with v0.117.0

Released 2026-09-05.

### Go APIs

| Affected API | Incompatible change | Migration |
| --- | --- | --- |
| `rod.Browser.EachEvent`, `rod.Page.EachEvent` | Accept `...rod.EventHandler` instead of arbitrary callback functions. Callback signature inspection is removed. | Wrap every callback with `rod.On`. Its signature is `func(*E, proto.TargetSessionID) bool`, where `E` is a concrete protocol event type. Add the session parameter even when unused; return `true` to stop waiting or `false` to continue. Multiple event types can still be subscribed to together. |
| `rod.Browser.WaitEvent`, `rod.Page.WaitEvent` | Generic methods accept `*E` instead of `proto.Event`; passing an event value just to wait for its name no longer compiles. | Declare a concrete event variable and pass its address: `var event proto.PageLoadEventFired; wait := page.WaitEvent(&event)`. |
| `rod.Message.Load` | Generic method requires a concrete event output pointer. Passing an event value for a name-only check is removed. | Use `message.Load(&event)` to decode. For a name-only check, compare `message.Method == (proto.PageLoadEventFired{}).ProtoEvent()`. |
| `launcher.ResolveURL`, `launcher.MustResolveURL` | Require a context as their first argument. | Replace `ResolveURL(endpoint)` with `ResolveURL(ctx, endpoint)`, and likewise for `MustResolveURL`. |
| `jsonvalue.Num`, `jsonvalue.Int`, `jsonvalue.Str`, `jsonvalue.Bool` | Removed. | Use Go's `new(expr)`. The respective replacements include `new(float64(value))`, `new(int(value))`, `new(string(value))`, and `new(bool(value))`; omit conversions when the expression already has the required type. |
| `assets.MousePointer`, `assets.Monitor`, `assets.MonitorPage` | Changed from string constants to embedded string variables. | Read them as ordinary strings. Replace dependent constant declarations with variables; they cannot be used in constant expressions. |

Generic methods cannot implement interface methods. If an application interface
previously included `WaitEvent` or `Load`, use the concrete receiver or an adapter
that exposes a fixed event type.

For example, migrate an event callback as follows:

```go
// v0.117.0
wait := page.EachEvent(func(event *proto.PageLoadEventFired) bool {
    return true
})

// v0.118.0
wait := page.EachEvent(rod.On(func(event *proto.PageLoadEventFired, _ proto.TargetSessionID) bool {
    return true
}))
```

### Runtime behavior

| Affected API | Incompatible change | Migration |
| --- | --- | --- |
| `launcher.ResolveURL`, `launcher.MustResolveURL` | Discovery has a 10-second maximum, honors caller cancellation, checks HTTP status, and rejects missing or invalid WebSocket URLs. Body-read, JSON, and URL errors are returned by `ResolveURL` instead of panicking or accepting an unusable result. | Handle the returned error and pass the caller's context. `MustResolveURL` still panics on errors. Discovery services must respond within the bound with a valid `ws` or `wss` URL. |
| Discovery through `http.DefaultClient` | `ResolveURL` creates its own HTTP client, so custom transports, redirect policies, cookie jars, and timeouts assigned to `http.DefaultClient` no longer configure discovery. | `http.DefaultTransport` still applies. For client-specific discovery, make the request yourself and pass the resulting WebSocket URL to `Browser.ControlURL`. |
| `cdp.Client.Call` | Request JSON encoding failures return errors. Malformed incoming CDP JSON terminates the reader and fails pending and subsequent calls instead of panicking in the reader goroutine. Calls after a terminal read failure return that retained error, such as `io.EOF`, without another transport write. | Handle call errors and create a new client/connection after terminal failure. Do not depend on `recover` for encoding failures or retry by reusing a failed client. |
| `cdp.Client` with a custom transport implementing `io.Closer` | The client closes that transport after its reader terminates. | Give the client ownership of the transport; do not share it with another independent connection owner. |
| `cdp.WebSocket.Connect` | Cancellation and deadlines now interrupt the entire connection handshake; failed connections close and clear their internal connection. | Handle context errors during establishment. Call or defer `Close` only after `Connect` succeeds; calling it after a failed handshake can panic. The context owns establishment only: use `Close` to end a successfully connected transport. |
| `launcher.Launcher.Launch` | A newly started browser is killed if subsequent endpoint discovery fails. | Treat a launch error as failure to obtain a running, usable browser. |
| `utils.OutputFile` | Directory/open errors are returned, and reader-backed output files are closed with copy/close errors joined. | Check the returned error and use `errors.Is`/`errors.As` for underlying I/O errors instead of relying on a specific top-level error type. |
| `rod.Message`, `rod.Message.Load` | `Message` contains a value mutex and must not be copied after first use. First decoding replaces the destination from a fresh zero value, clearing fields absent from JSON. Cached events are shallow copies. | Share `*Message`. Do not prepopulate event destinations as defaults; apply defaults after loading. Treat slices, maps, and nested pointers in decoded events as read-only. |
| Page contexts after `Target.targetDestroyed` | The event now decodes its target ID and cancels the matching page context. | Stop work on a destroyed page and handle context cancellation; create or obtain another page for further work. |
| `rod.Element.WaitStable`, `rod.Element.WaitStableRAF` | Typed shape comparison treats nil and empty coordinate slices as equal. A NaN coordinate is unequal even when two inputs share the same slice. | Do not use nil-versus-empty slices as a geometry change signal. Animation-frame waiting still distinguishes an uninitialized result from an initialized empty result. |

### Development workflows

| Affected command or file | Incompatible change | Migration |
| --- | --- | --- |
| `go run ./lib/assets/generate`, `fixtures/mouse-pointer.svg` | The asset generator is removed and the SVG moved into `lib/assets`. | Edit `lib/assets/mouse-pointer.svg`, `monitor.html`, or `monitor-page.html`; Go embeds them at build time. Remove asset-generation steps from local scripts. |
| Build contexts and source distributions | Building `lib/assets` now requires its HTML and SVG source files. Copying only Go files no longer works. | Include `lib/assets/monitor.html`, `lib/assets/monitor-page.html`, and `lib/assets/mouse-pointer.svg` in build contexts and source packages. |
| `go generate ./...`, protocol and device generators | Default generation reads pinned local snapshots; the root command also regenerates devices. Installing a different browser no longer selects the protocol schema. | Follow the explicit snapshot update procedures in [protocol generation](../lib/proto/generate/README.md) and [device generation](../lib/devices/README.md). |

## v0.117.0 — compared with upstream v0.116.2

Released 2026-09-05. This is the first release of the `github.com/rah-0/rod` fork.
The upstream baseline version is recorded in the annotated `v0.117.0` tag.
The fork changes were audited from upstream parent commit `d38c75327872` to
release commit `420e210ee5e3`; the parent is not a locally tagged upstream release.

### Go APIs and requirements

| Affected API or requirement | Incompatible change | Migration |
| --- | --- | --- |
| Module and package imports | Module path changed from `github.com/go-rod/rod` to `github.com/rah-0/rod`. | Update `go.mod` and all Rod imports, including subpackages. |
| Go toolchain | Minimum Go version increased from 1.21 to 1.27.1. | Build and test with Go 1.27.1 or later. |
| Public `gson.JSON` results, callbacks, and protocol fields | Replaced by `jsonvalue.Value`, a distinct type in `github.com/rah-0/rod/lib/jsonvalue`. | Update explicit types, constructors, and callback signatures. This includes `Element.Property`/`MustProperty`, `Page.MustEval`, `Element.MustEval`, `Page.ObjectToJSON`/`MustObjectToJSON`/`MustObjectsToJSON`, `Page.Expose`/`MustExpose`, and `HijackRequest.JSONBody`. Protocol fields/types include `RuntimeRemoteObject.Value`, `RuntimeCallArgument.Value`, `AccessibilityAXValue.Value`, and `NetworkHeaders`. |
| `launcher.Browser`, `launcher.NewBrowser` | Downloader type and constructor removed, including fields `Context`, `Hosts`, `Revision`, `RootDir`, `Logger`, `LockPort`, `HTTPClient` and methods `Dir`, `BinPath`, `Download`, `Get`, `MustGet`, `Validate`. | Provision a browser outside Rod and launch it with `launcher.New()`, optionally setting `Launcher.Bin`. |
| `launcher.Host`, `HostGoogle`, `HostNPM`, `HostPlaywright`, `DefaultBrowserDir` | Download host and cache configuration removed. | Remove browser-download configuration from application code. |
| `launcher.Launcher.Revision`, `launcher.RevisionDefault`, `launcher.RevisionPlaywright`, `defaults.LockPort` | Revision selection and download-lock configuration removed. | Select the installed browser executable using `Launcher.Bin`; manage browser versions outside Rod. |
| `launcher.Launcher.Leakless`, `flags.Leakless` | Removed with the helper executable. | Give the application explicit browser shutdown ownership; see the lifecycle entry below. |
| `launcher.NewManaged`, `launcher.MustNewManaged`, `launcher.NewManager` | Managed client constructors require `(serviceURL, authToken)`; the server constructor requires `(authToken)`. | Supply the same bearer token to clients and server. An empty server token rejects every request. |
| `launcher.Launcher.KeepUserDataDir`, `flags.KeepUserDataDir` | Remote profile-retention controls removed. | Remote profiles belong to the manager session and are deleted on disconnect. Use caller-owned profiles with local launchers when persistent filesystem state is needed. |

### Runtime and configuration

| Affected API or setting | Incompatible change | Migration |
| --- | --- | --- |
| `launcher.Launcher.Launch` | Browser binaries are no longer downloaded automatically. A missing installation returns `launcher.ErrBrowserNotFound`. | Install Chrome, Chromium, or Edge, or set `Launcher.Bin` to an existing executable. |
| Local browser lifecycle | Browsers launch directly; the removed `leakless` helper no longer guarantees cleanup after abrupt application termination. | Call `Browser.Close` or `Launcher.Kill` during shutdown. Arrange process supervision if cleanup after an application crash is required. |
| `launcher.Launcher.Cleanup` | Caller-supplied `UserDataDir` directories are preserved. | Delete caller-owned profiles explicitly when needed. Launcher-generated temporary profiles remain launcher-owned. |
| `rod-manager` authentication and listener | Requires `ROD_MANAGER_TOKEN` and defaults to `127.0.0.1:7317`. | Configure the token and connect through loopback, HTTPS/WSS, or a trusted encrypted tunnel. An explicit `-allow-plaintext-remote` flag is required for non-loopback plaintext server binding. |
| `launcher.NewManaged`, `launcher.MustNewManaged` transport | Non-loopback HTTP/WS URLs and HTTP redirects are rejected. | Use the final HTTPS/WSS endpoint directly, or a loopback tunnel. The server's plaintext override does not relax the managed client's transport policy. |
| Remote launch settings and `rod-manager --allow-all` | `--allow-all` is removed. Clients cannot choose the executable, environment, working directory, XVFB wrapper, profile directory, or debugging port. | Configure process settings on the trusted server through its defaults or `Manager.BeforeLaunch`. The manager reasserts its own profile and debugging port after that hook. |
| Remote `Launcher.ProfileDir`, `flags.Arguments`, and option names | Profile names must be a single relative child name. Raw arguments starting with `-` or `/`, malformed option names, and malformed option value lists are rejected. | Pass browser switches through named launch options and use a simple profile name within the manager-owned user-data directory. |
| `Manager.Defaults`, `Manager.BeforeLaunch`, and browser environments | `Authorization` is removed before hooks, `Rod-Launcher` is removed before `BeforeLaunch`, and `ROD_MANAGER_TOKEN` is stripped from browser child environments. | Read parsed launch settings from the hook's `Launcher`; do not depend on forwarded manager credentials or raw launcher headers. |
| Manager session lifecycle | Disconnecting the WebSocket terminates the remote browser and removes its temporary profile. | Keep the session connected while using the browser; do not rely on a remotely launched browser or profile surviving disconnection. |
| `launcher.MustNewManaged` HTTP/2 setting | No longer adds `disable-http2` automatically. | Set that browser flag explicitly if an application requires HTTP/2 disabled. |
| `defaults.Load`, exported defaults, `-rod` | Command-line defaults are no longer loaded by package initialization; constructors load them lazily. The `lock` option is unsupported and now panics. | Call `defaults.Load()` before `flag.Parse()` or assigning exported defaults. Remove `-rod=lock=...` configuration. Use `Reset` or `ResetWith` for explicit resets. |
| Bare `-rod=monitor` | Default monitor listener changed from all interfaces to `127.0.0.1:0`. | Specify a bind address explicitly if required. The monitor has no authentication; use an authenticated proxy for remote access. |
| `utils.UseNode` | Checks for an installed Node.js executable instead of downloading/installing one. | Provision Node.js before running generators or other Node-based tooling. |

See [launcher configuration and ownership](../lib/launcher/README.md) for current
setup instructions.
