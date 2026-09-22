# Breaking changes

Incompatible APIs, behavior, and development workflows are recorded here, newest
version first. Each entry compares that version with its previous release and
names the affected functions, types, fields, or commands with their migration.
When skipping releases, apply every intervening entry, oldest to newest.

Group changes under the version that introduces them, above earlier versions.
Each heading names that version and the previous version it changes. Describe
the implementation for the named version. Keep historical entries intact; add
later changes under their own version.

## v0.123.0 — compared with v0.122.0

### Retry timing

`DefaultSleeper` uses a 10 ms seed instead of 100 ms. The first retry occurs
after approximately 19–21 ms, then intervals grow with jitter up to one second.
Short waits respond sooner, with more evaluations early in longer waits. Supply
an explicit sleeper through `Browser.Sleeper`, `Page.Sleeper`, or `Element.Sleeper`
when an application requires a particular retry cadence.

`utils.BackoffSleeper` caps each computed interval at `maxInterval`, including
intervals returned by a custom backoff algorithm. Algorithms can no longer
overshoot that configured limit.

## v0.122.0 — compared with v0.121.0

### Runtime behavior

| Affected behavior | Change | Migration |
| --- | --- | --- |
| `Page.WaitOpen`, `Page.MustWaitOpen` | A pending popup wait ends when the opener's target is destroyed or its session is detached. `WaitOpen` returns `context.Canceled`; the Must helper panics on that error. | Handle opener termination when waiting for a popup. A successfully returned popup keeps the caller's operation context and remains usable after the opener closes. |

### Test workflows

Public API browser integration tests live in `tests/`, `tests/lib/cdp`, and
`tests/lib/launcher`. Running `go test .` or testing only the source library
directories no longer runs those integration tests. Use
`bash scripts/check.sh browser` for the complete browser suite. Direct runs from
the new test directories need `-coverpkg` to measure the corresponding source
package; see [test commands](../tests/README.md). Public APIs are unchanged by
this relocation.

## v0.121.0 — compared with v0.120.0

The generated protocol API follows Chrome `152.0.7977.64`, replacing the
Chrome `128.0.6568.0` schema. [The protocol migration inventory](PROTOCOL_CHANGES.md)
lists every removed type and enum constant, changed field type or JSON tag,
and newly required field on an existing type, with migration guidance.
Use keyed protocol struct literals and update callers of removed APIs.

Protocol generation now reads the installed browser by default. Keep an
installed Chrome or Chromium executable available when regenerating; use
`-schema` to reproduce an explicit offline schema. Ordinary builds use the
committed Go declarations. Review and accept the schema and generated API
changes together. Run `go run ./lib/proto/generate -check` for a read-only
installed-browser drift check. `scripts/check.sh browser` runs that check and
the runtime tests, preserving failures from either. See
[protocol generation](../lib/proto/generate/README.md) for the complete workflow.

### Fixture workflows

`fixtures/gen-fonts` is removed. Edit the static multilingual sample in
[`fixtures/fonts.html`](../fixtures/fonts.html) directly; regenerating it no
longer requires Google Translate. `TestFonts` creates its PDF using the installed
browser on ordinary hosts as well as containers.

The skipped `Example_load_extension` is replaced by the tested
[`examples/load-extension`](../examples/load-extension/main.go) command. Run it
from the repository root with `go run ./examples/load-extension`. The fixture
uses Manifest V3 and a local HTTP page, loaded through `Extensions.loadUnpacked`.

## v0.120.0 — compared with v0.119.0

### Go APIs

| Affected API | Change | Migration |
| --- | --- | --- |
| Optional boolean fields on generated CDP commands | Use `*bool` to distinguish omission from explicit `false`. Required booleans and response/event booleans retain their types. | Replace command literals such as `FromSurface: false` with `FromSurface: new(false)`. Use `nil` for Chrome's default. Check for nil before dereferencing optional fields. |
| `Browser.EnableDomain`, `Browser.DisableDomain`, `Page.EnableDomain`, `Page.DisableDomain`, `Page.SetExtraHeaders` | Return `(func() error, error)` so setup and restoration failures are observable. Restoration is bounded and idempotent. `DisableDomain` restores the original enable configuration. | Check the setup error before using the restore function, then check `restore()` when finished. `MustSetExtraHeaders` retains `func()` and panics on either error. |
| `Browser.EachEvent`, `Page.EachEvent`, `Browser.WaitEvent`, `Page.WaitEvent`, `Page.WaitNavigation`, `Page.WaitRequestIdle` | Return `func() error`; setup, cancellation, connection loss, and restoration failures are observable. | Check `err := wait()`. Change stored callback types to `func() error`. The corresponding `Must` helpers retain `func()` and panic on errors. |
| `Page.HandleDialog` | Its wait function returns `(*proto.PageJavascriptDialogOpening, error)` rather than an event alone. | Check the wait error before reading the event or handling the dialog. `MustHandleDialog` keeps its existing shape and panics on wait errors. |
| `Browser.WaitDownload` | Returns `(func() (*proto.BrowserDownloadWillBegin, error), error)`. The result uses the Browser event type, pins the first matching GUID, and reports canceled downloads. Each browser context permits one active wait. | Check both setup and wait errors. Handle `ErrDownloadCanceled` and `ErrDownloadInProgress`; complete or cancel the previous wait before starting another in the same context. `MustWaitDownload` still returns file bytes. |
| `SearchResult.Release` | Returns an error and performs bounded, idempotent cleanup even after the search context expires. | Check `result.Release()`; release successful search results when finished. Failed searches clean up automatically. |
| `HijackRouter.Run` | Returns an error for failed setup, event waiting, or cleanup. Errors serving individual requests still use `Hijack.OnError`. | Collect the result from the goroutine running the router and handle it alongside `Stop`; configure `OnError` for request failures. |
| `launcher.NewManaged`, `launcher.MustNewManaged` | Require the caller context before the service URL and token. Initialization honors that context through discovery and body reads. | Use `NewManaged(ctx, serviceURL, token)` or `MustNewManaged(ctx, serviceURL, token)` with a suitable deadline. |
| `input.Numpad0` through `input.Numpad9`, `input.NumpadDecimal` | Numeric key IDs change to match the corrected keypad virtual-key codes. | Use the named constants rather than storing their integer values. Replace previously serialized IDs with the corresponding named key. |

### Runtime behavior

| Affected behavior | Change | Migration |
| --- | --- | --- |
| `Browser.PageFromTarget` and page contexts | Calls return separate views of a shared attachment using the current caller context. `Page.GetContext` and cloned `Keyboard`, `Mouse`, and `Touch` operations use that context while sharing attachment and input state. Session termination independently cancels operations and event streams. | Compare target/session IDs rather than Page pointer identity. Use `Page.Event` or operation errors to observe session closure. An expired view does not invalidate later views. |
| `Browser.Pages` on an incognito browser | Returns only pages belonging to that browser context. The root browser still lists pages across contexts. | Use the root browser for cross-context enumeration. |
| `Page.Close` | Returns cancellation or connection-loss errors if target destruction is unconfirmed. A rejected beforeunload prompt still returns `PageCloseCanceledError`. | Check the close error before assuming the target was destroyed. |
| `Element.Frame` | Cross-process frames use their own renderer session. A frame view belongs to its current document and renderer; obsolete views return a session error or `ErrFrameContextChanged`. | Obtain another view with `Element.Frame` after a renderer transition. Use `errors.Is(err, rod.ErrFrameContextChanged)` to recognize a stale document context. |
| `RuntimeCallArgument.Value` | An unset `jsonvalue.Value` is omitted, preserving JavaScript `undefined` and object/unserializable arguments. Explicit `jsonvalue.New(nil)` remains JSON null. | Use `jsonvalue.New(nil)` when passing an explicit null. Leave `Value` unset when supplying `ObjectID` or `UnserializableValue`. |
| `Element.Screenshot` | Captures the transformed element bounds using native clipping and the browser's scale. Bounds round outward to device-independent pixel edges. | Expect scaled bitmap dimensions and correct fractional edges; update image expectations that depended on the former CSS-coordinate crop. |
| `HijackRequest` getters and `SetBody` | Getters reflect the outgoing HTTP request and latest replacement body. `Headers` returns a snapshot. Captured/replacement bodies set replay metadata for redirects; available binary post-data entries are preserved. | Treat `Body()` as raw bytes held in a Go string; use `[]byte(request.Body())` for binary consumers. Save original values before modifying them, and use `Req().Header` to change headers. `SetBody` replaces `GetBody` and `ContentLength`; install custom replay behavior afterward. |
| `HijackResponse.SetHeader` | Replaces every existing value of a header case-insensitively. `LoadResponse` preserves repeated headers. | Use `AddHeader` when adding another value, including multiple `Set-Cookie` headers. |
| `HijackRouter.Add`, `proto.PatternToReg` | URL patterns use CDP glob syntax with literal regexp punctuation. Resource-type filters also apply to local dispatch. A fulfilled request stops the handler chain; unmatched or fully skipped requests continue. | Use `*` and `?` for wildcards and backslash for escaping. Do not pass regular expressions. Set `Hijack.Skip` to advance to another matching handler. Add or remove routes before stopping the router; stopped routers reject updates. |
| `cdp.WebSocket` | Uses valid random handshake keys and frame masks, preserves outgoing buffers, handles control/fragmented text frames, and rejects malformed framing or conflicting header overrides. | Custom peers must implement WebSocket framing correctly. Supply a valid base64-encoded 16-byte nonce when overriding `Sec-WebSocket-Key`; leave it unset to generate one. |
| `cdp.Client.Close` | Explicit closure terminates pending calls with `ErrClientClosed`, unless a prior terminal error was already recorded, and closes a custom transport at most once. The event channel closes when its reader exits. | Treat Close as terminal; create another client for a new connection. A transport without `io.Closer` returns `ErrTransportNotClosable`. Its `Close` must interrupt blocked reads and writes. |
| `cdp.Client.Call` transport writes | Uses an optional `SendContext(context.Context, []byte) error` transport method. Canceling an active built-in WebSocket write closes the connection because a partial frame cannot be resumed safely. | Reconnect after an interrupted write. Custom transports should implement `SendContext` to honor request cancellation; transports exposing only `Send` must bound that operation themselves. |
| Owned `Browser.Close` | Its ten-second default budget covers graceful shutdown and forced cleanup together. `CloseWithTimeout` selects another total budget. | Handle deadline errors when cleanup cannot finish within the budget; retry cleanup with an adequate budget if needed. |
| `Page.Reload`, `Browser.HandleAuth`, `Page.HandleFileDialog` | Propagate setup, wait, and restoration failures. File-chooser interception restores its prior state on completion or cancellation, including canceled unused waits. | Check returned errors instead of assuming an absent event means success. Give unused file-chooser waits a cancelable context. Authentication waits restore the prior Fetch configuration when invoked and completed. |
| `Page.WaitDOMStable`, `Page.WaitStable` | Reject nonpositive stability durations with an error; `WaitRequestIdle` rejects negative durations. | Pass a positive stability interval and use a context deadline for the maximum wait. |

### Examples

Runnable examples live in the root [examples directory](../examples).
`lib/examples` and its obsolete external-service demonstrations are removed.
Update commands and nested-module paths to `examples/...`; the retained examples
use local fixtures and include tests.

## v0.119.0 — compared with v0.118.0

### Browser lifetime

| Affected behavior | Change | Migration |
| --- | --- | --- |
| Unix `Launcher.Launch` | A supervisor now owns each started browser and stops it when the launching application exits, including abnormal exit. Linux also reaps detached descendants. | Keep the owning application alive for the browser's intended lifetime. See [launcher platform and initializer requirements](../lib/launcher/README.md). |
| Generated temporary profiles | Removed automatically when the browser exits or startup fails. `Cleanup` waits for removal. | Supply `UserDataDir` explicitly when profile data must persist. Caller-selected profiles are never removed. |
| Unix browser `TMPDIR` | Each supervised browser receives a private child directory, removed after its descendants exit. | A `TMPDIR` supplied through `Launcher.Env` selects the parent. Do not rely on the browser seeing that exact value; the caller's parent directory and existing files are preserved. |
| Automatically launched `Browser` | Cancellation of the original `Connect` context or loss of its CDP connection stops the owned browser. `Close` uses an independent five-second graceful shutdown budget before forced cleanup. | Give `Connect` a context spanning the desired browser lifetime; use later context clones for individual operations. URL-attached browsers retain their existing ownership semantics. |
| Launcher startup diagnostics | Internal output capture retains at most 64 KiB before the DevTools endpoint appears. | Use `Logger` for complete output, with a writer that does not block indefinitely. |

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
