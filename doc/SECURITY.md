# Security

This document describes what Rod protects against, the protections that are on
by default, and the options that turn them off. Every opt-out is explicit and
named below, together with the risk it brings; a program that uses one accepts
that risk. For how these defaults changed from earlier versions, see
[Breaking changes](BREAKING.md#v01290--compared-with-v01280).

## Threat model

### What Rod defends against

Rod treats these sources as untrusted:

- **Web pages** loaded in the automated browser: their DOM and text, the values
  their JavaScript returns, the events they cause, calls to exposed functions,
  downloads, and network responses, including responses that
  `Hijack.LoadResponse` loads from Go.
- **The browser endpoint**: a remote, proxied, or compromised browser or
  DevTools endpoint, and anyone who can read or change a plaintext connection to
  it.
- **Other local users** of a shared machine, who can read process lists, create
  files in shared temporary directories, and connect to loopback ports.
- **Remote clients of the launcher manager** (`rod-manager` and
  `launcher.Manager`).

Against these sources, Rod:

- returns errors instead of panicking in non-`Must` APIs when pages, the
  network, or the endpoint send malformed or hostile data. `Must` helpers still
  panic through the configured fail function;
- validates protocol data and bounds the data it accepts from the endpoint, from
  responses that Go loads, and in the scroll screenshots it stitches;
- sends HTTP credentials only to the challenger they name, keeps typed text out
  of trace output, and does not send Go panic values to pages;
- creates the profiles and download directories it owns at unpredictable
  paths, with owner-only permissions on Unix; on Windows they inherit the
  permissions of their parent directory;
- requires a secret for the monitor and the manager, and validates the launch
  options of manager clients as defense in depth.

A malicious endpoint still controls what the browser reports. Validation and
resource budgets constrain the processing described below; they do not
authenticate page data or guarantee correct results.

### What Rod does not defend against

- **The automation code itself**: what it does with page data, what exposed
  functions and hijack handlers do, and the opt-outs it enables.
- **The browser's own security**: the renderer sandbox, site isolation, the
  same-origin policy, and browser bugs. Rod relies on the browser to contain
  page content, and its default launch flags preserve these protections;
  see [Launcher defaults](#launcher-defaults).
- **Anyone who holds the DevTools connection or the manager token.** Treat them
  as administrators of the browser host: they control every page, profile, and
  cookie of the browser, can read the files the browser can read, and can use
  its network access. Rod does not restrict them.

## Secure defaults and opt-outs

### Request hijacking

`HijackRouter` handlers, from `Page.HijackRequests` and
`Browser.HijackRequests`, receive the requests that reach the Fetch domain of
their target; see [what routes do not see](#what-routes-do-not-see).
`Hijack.LoadResponse` and `Hijack.MustLoadResponse` send such a request from Go
and use the response to fulfill it.

| Protection | Default | Opt-out | Risk when changed |
| --- | --- | --- | --- |
| Redirects of responses loaded by Go | A 3xx response and its `Location` return to the browser. | Set `Hijack.FollowRedirects = true`. The safe transport permits at most ten same-origin redirects and validates each destination at dial time. | `LoadResponseUnsafe` instead uses the supplied client's redirect policy, which can expose internal services or send credentials to another origin. |
| Response body size | `Hijack.MaxResponseBodyBytes` starts at `DefaultMaxResponseBodyBytes` (64 MiB); zero or a negative value selects that default. A larger `Content-Length` fails before reading; otherwise reading stops one byte past the limit. The error matches `ErrResponseBodyTooLarge`, and `Hijack.Response` stays unchanged. | Raise `MaxResponseBodyBytes` in the handler; `math.MaxInt64` removes the limit. | Each concurrent `LoadResponse` buffers up to the limit. Fulfilling the request holds two more copies of the body, base64 encoded, together about 2.7 times its size: the protocol message and its WebSocket frame. Chrome closes the whole DevTools connection when it receives a message larger than about 100 MiB, which a body above about 74 MiB, or a smaller body with large headers, produces. |
| Response header size | `Hijack.MaxResponseHeaderBytes` starts at `DefaultMaxResponseHeaderBytes` (256 KiB, counted as an HTTP/1.1 header block), the size at which Chrome rejects response headers from the network. Larger headers return an error matching `ErrResponseHeadersTooLarge`, and `Hijack.Response` stays unchanged. The client's transport applies its own limit first; `http.Transport` allows 10 MiB by default. | Raise `MaxResponseHeaderBytes` in the handler. | More memory per request, and the page receives headers that Chrome rejects from the network. The headers and the base64 encoded body share Chrome's message limit. |
| Unparsable request URLs | A paused request whose URL `net/url` cannot parse, such as a path with `%zz` or a stray `%` as in `/sale-50%-off`, or a host name containing `{`, fails with `net::ERR_FAILED`. No handler runs, and the parse error goes to the router's `OnError`. `HijackRequest.URL` is therefore never nil. | `HijackRouter.ContinueUnparsableURLs(true)` continues such requests unmodified. Handlers still do not run, and the parse error is still reported. | A page can evade any route by adding an invalid escape, or such a host name, to a URL that still matches the route's pattern: the request reaches the network without the blocking, mocking, or rewriting that the route applies. Enable it only when no route enforces a policy that the page must not evade. |
| Handler panics and `runtime.Goexit` | Always recovered, including panics from `Must` helpers such as `MustLoadResponse`. Unless the handler had started resolving the request, the router fails it with `net::ERR_FAILED`; each paused request is resolved once. `Hijack.OnError` receives a `*TryError`, which unwraps to the panic value, or `ErrHijackHandlerExited`. | To stop the process, re-panic from `OnError`. A panic in `OnError` is not recovered; the request is already resolved by then. | The process ends on a handler failure that a page can trigger. |

By default, the router discards errors, so blocked, failed, and oversized
requests are silent. `HijackRouter.OnError(fn)` sets a router-wide handler: it
is the initial `Hijack.OnError` of each later request and also receives errors
for requests that the router resolves without running handlers. It is called
concurrently from the goroutines that serve requests.

**Go network requests have their own policy.** `LoadResponse(nil, ...)` and
`MustLoadResponse` use a direct transport with certificate verification, no
proxy, a 30-second request deadline, bounded connection and header deadlines,
and at most eight concurrent fetches across the process. The dialer rejects
private, loopback, link-local, multicast, unspecified, shared and special-purpose
addresses, including mixed public/private DNS answers. It connects to the checked
numeric address, preventing a second DNS lookup from changing the destination.
`Hijack.DenyNetworks` adds deployment-specific ranges. `FollowRedirects` permits
only the original scheme, host and effective port. Policy failures match
`ErrHijackNetworkPolicy`; exhausted admission matches `ErrHijackConcurrency`.

A custom client passed to `LoadResponse` returns `ErrHijackUnsafeClient`.
`LoadResponseUnsafe(client, ...)` and `MustLoadResponseUnsafe` explicitly delegate
network, proxy, TLS, timeout and redirect policy to the caller. A nil unsafe
client uses `http.DefaultClient`. These requests can expose internal services to
pages. Use `ContinueRequest` when the browser should perform the request.
Even the safe transport cannot identify deployment-specific sensitive services
on public addresses; enforce those restrictions with `DenyNetworks` or network
egress controls.

**Handler concurrency is bounded.** Routers admit 64 handlers by default;
`HijackRouter.MaxConcurrentRequests` changes this limit. Excess requests fail
closed and report `ErrHijackConcurrency` through `OnError`. Lower response byte
limits for workflows that do not need large resources, and bound callback work
with the request context.

#### What routes do not see

`Page.HijackRequests` intercepts only the requests of its page's target. It does
not see requests from windows that the page opens or from service workers. With site isolation enabled, it also misses cross-site iframes, which
run in their own targets. No router sees WebSocket connections. In Chrome 152, a
page escaped a page-level route that blocked its requests through
`window.open`, a service worker's `fetch`, and `new WebSocket`, and, with site
isolation enabled, through a cross-site iframe. The same route blocked the
page's own `fetch`, a dedicated worker's `fetch`, and `navigator.sendBeacon`.
`Browser.HijackRequests` blocked all of these requests except the WebSocket. To
enforce a policy on untrusted pages, use `Browser.HijackRequests`, and block
WebSocket endpoints outside Rod, such as in a proxy that the browser uses.

### HTTP authentication

`Browser.HandleAuth` and `Browser.MustHandleAuth` take
`AuthCredentials{Source, Origin, AnyOrigin, Username, Password}`.

| Protection | Default | Opt-out | Risk when changed |
| --- | --- | --- | --- |
| Credential scope | Only a challenge whose `Source` (`proto.FetchAuthChallengeSourceServer` for HTTP 401, `proto.FetchAuthChallengeSourceProxy` for HTTP 407) and `Origin` both match receives the credentials, which ends the wait. Other challenges receive the browser's default response; headless Chrome fails those requests with `net::ERR_INVALID_AUTH_CREDENTIALS`. | `AnyOrigin: true` with an empty `Origin`: the first challenge from `Source` receives the credentials, whatever its origin. It is meant for challengers that cannot be named in advance, such as a proxy that a PAC script selects. | With `Source` set to Server, any server the browser loads a resource from during the wait, including a hostile page's own server, a third-party resource, or a redirect target, can challenge first and receive the username and password. With `Source` set to Proxy, the credentials go to whichever proxy that Chrome sends requests through challenges first; Chrome fails a 407 response from a server that it reaches directly. Source scoping remains: proxy credentials never answer a server challenge, and server credentials never answer a proxy challenge. |

`Origin` is `scheme://host[:port]`, without user information, path, query, or
fragment. Both origins are compared in lowercase with an explicit port: 80 for
http and 443 for https, while other schemes need a port. Give internationalized
host names in their Punycode form. Invalid credentials return an error matching
`ErrAuthCredentials` before the Fetch configuration changes, and the error does
not repeat user information from the origin.

A matching `http://` server origin still receives Basic credentials
unencrypted. The wait enables Fetch for every request and continues paused
requests unmodified, so run at most one wait per browser, and no `HijackRouter`
or other Fetch interception during it. Each wait answers one challenge; cancel
its context when the matching challenge may never come.

Headers from `Page.SetExtraHeaders` have no such scope: the browser adds them to
every request of the page, to every origin, including third-party resources. Do
not use it for credentials when a page can load from origins you do not trust;
use `HandleAuth`, or add the header in a `HijackRouter` route that matches only
the intended origin.

### Protocol decoding

| Protection | Default | Opt-out | Risk when changed |
| --- | --- | --- | --- |
| Required protocol fields | `proto.DecodeStrict`, the zero value of `proto.Decoding`. The generated `Call` methods, `proto.Unmarshal`, `Message.Load`, and the results and events that Rod decodes itself reject, at any depth, a member that the protocol requires and that is missing or `null`, and an array that contains `null`. The error matches `proto.ErrMissingField` and is a `*proto.MissingFieldError` that names the type and the JSON path. | `Browser.Decoding(proto.DecodeLenient)`, set before `Browser.Connect`, for a browser, its pages and elements, and the events it receives. `proto.DecodeLenient.Unmarshal` for other data. A `proto.Client` that implements `proto.Decodable` for its command results. | Missing members keep their zero value, and missing objects and array entries are nil. See [lenient decoding](#lenient-decoding). |

Strict decoding does not require members that the schema marks as experimental
or deprecated, or members that older browsers omit, which
`lib/proto/generate/schema-compatibility.json` lists. It accepts `null` for
`jsonvalue.Value` values and for `float64` numbers, which Chrome sends as
`null` when JSON cannot represent them. Member names must match the protocol's
names exactly, including case, and invalid UTF-8 in strings and map keys is
replaced with U+FFFD. These values are not checked: the bytes that
`Browser.Call` and `Page.Call` return, command parameters, types of other
packages that embed a generated type, and protocol types decoded directly with
`encoding/json`. See [required fields](../lib/proto/README.md#required-fields).

#### Lenient decoding

With lenient decoding, Rod's own non-`Must` APIs still:

- return an error matching `proto.ErrMissingField`, instead of panicking,
  hanging, or returning a nil result, when an object, array, or object entry
  that they use is missing;
- treat an empty identifier, or execution context ID 0, as missing where they
  would use it to address a browser context, target, session, search, script,
  or execution context. For example, an incognito `Browser` never acts on the
  default context, and page commands never go to the browser session;
- fail closed: `HijackRouter` fails a paused request without request details
  and never continues it, even with `ContinueUnparsableURLs`. `HandleAuth` gives
  the default response to a challenge without its details or its source, and,
  unless `AnyOrigin` is set, without its origin. `Page.Expose` ignores a binding
  call without an execution context. `Browser.WaitDownload` ignores targets
  without their information or ID and downloads without a frame ID. Page
  diagnostics report `ErrDiagnosticsIncomplete`.

Code that uses lenient decoding must check the rest:

- Other missing members act as values the endpoint sent. For example, a
  `Fetch.requestPaused` event without `resourceType` matches no route that
  filters on a resource type, so the router continues the request unmodified.
  Missing IDs that events are matched by can match the wrong request or frame.
- A `null` entry of an array of numbers, strings, or booleans reads as 0, an
  empty string, or false.
- Values that Rod returns or passes on can hold nil objects and entries, such
  as the node from `Element.Describe`, the result of
  `Page.GetNavigationHistory`, and the events that `EachEvent` handlers,
  `WaitEvent`, `Browser.Event`, `Page.Event`, and `Message.Load` deliver.
- Code that calls the generated `Call` methods or `Unmarshal` itself receives
  unchecked values, and helpers such as `proto.CookiesToParams` panic on a nil
  entry.
- `Must` helpers dereference the nil or zero result they got when the fail
  function does not panic.

The checks prevent crashes and misrouting, not wrong values that are present.
Use lenient decoding only for a trusted endpoint that is known to omit fields.

### CDP transport

The built-in `cdp.WebSocket` bounds what it accepts from the browser endpoint.
`Browser.Connect` uses it with the defaults below. To change them, connect a
`cdp.WebSocket` yourself and pass `cdp.New().Start(ws)` to `Browser.Client`.

| Protection | Default | Opt-out | Risk when changed |
| --- | --- | --- | --- |
| Incoming message size | `cdp.WebSocket.MaxMessageSize`: zero or a negative value selects `cdp.DefaultMaxMessageSize` (256 MiB), counting all fragments. A larger message fails with `cdp.ErrWebSocketMessageTooLarge`, closes the connection with status 1009, and fails pending and later calls. | A larger `MaxMessageSize`; `math.MaxInt64` accepts any size. | The endpoint can make the process hold messages of that size. A frame header alone allocates at most 256 MiB; longer payloads grow as their bytes arrive. |
| Handshake duration | `cdp.WebSocket.HandshakeTimeout`: zero selects `cdp.DefaultHandshakeTimeout` (30 seconds) for dialing, TLS, and the HTTP upgrade, in addition to the context. The error matches `context.DeadlineExceeded`. | A negative `HandshakeTimeout` leaves only the context in effect. | An endpoint that never completes the upgrade blocks `Connect` until the context ends. |
| Handshake response size | At most 1 MiB, headers included; a larger response fails with `cdp.ErrWebSocketProtocol`. | None. | — |
| Discovery response size | `launcher.ResolveURL` rejects a `/json/version` body larger than 1 MiB and bounds the request, including the body, to 10 seconds. | Resolve the WebSocket URL yourself and pass it to `Browser.ControlURL`. | — |
| Manager settings size | `launcher.NewManaged` rejects a settings response larger than 1 MiB. | None. | — |
| TLS verification | `wss://` URLs use `crypto/tls` with its default configuration, which verifies the certificate chain against the system roots and the host name. | `cdp.WebSocket.Dialer`. A custom dialer replaces this and must perform TLS itself for `wss://` URLs. | A dialer that skips verification, such as with `InsecureSkipVerify`, lets anyone on the network path impersonate the endpoint and control the browser. |

Pages control the size of many messages: evaluation results, `Page.HTML`,
screenshots, and events such as console messages and exposed-function calls
while the Runtime domain is enabled. A message above `MaxMessageSize` that a
page causes therefore ends the connection, and with it the automation of every
page of the browser. In Chrome 152, with `MaxMessageSize` lowered to 1 MiB
and the Runtime domain enabled, a page's own `console.log` of a 2 MiB string
ended the connection.

Invalid JSON or an invalid WebSocket frame from the endpoint ends the
connection: pending and later calls return the error, and the event channel
closes. When a context interrupts a write before the connection accepts any byte
of the frame, the frame is abandoned and the connection stays usable. A
partially written frame, any failed TLS write, and other write errors close the
connection, so a later request never follows an incomplete frame.

Rod accepts plain `ws://` and `http://` endpoints, including remote ones; `launcher.NewManaged` rejects every plaintext endpoint, including loopback. Anyone who can read
or change such a connection sees all protocol traffic and can control the
browser. Reach remote browsers through `wss://` or an encrypted tunnel.

### Exposed functions

`Page.Expose(name, fn, onError)` and `Page.MustExpose` install
`window[name]`, which calls `fn` in Go with a JSON argument from the page.

- **Reach.** `Expose` accepts calls only from the receiver's frame. The source
  comes from CDP execution-context metadata, including its system-unique
  document ID; stale documents and unknown contexts are rejected before Go
  runs. `ExposeWithOptions` supplies `ExposedCall.Source` and allows exact frame,
  origin and document allowlists. `AllFrames` explicitly broadens frame scope.
  An exposure survives navigation unless its origin or document policy prevents
  it. Every script in an allowed document can call it, including third-party
  scripts, so validate arguments and grant only capabilities that document
  needs. Direct calls to the internal binding receive the same authorization.
- **Call rate and memory.** Calls execute serially. Event ingress is bounded by
  the subscription budgets below; pending reply queues are bounded too.
  `ExposeOptions.MaxPayloadBytes` and `MaxReplyBytes` default to 1 MiB each.
  Queue overflow stops the exposure and reports a limit error through `onError`.
  Oversized payloads are ignored and reported; oversized results reject the
  promise and are reported. Go callbacks must cooperate with cancellation; Rod
  cannot interrupt arbitrary Go code or prevent a callback from allocating its
  result before encoding it.
- **Failures.** A panic in `fn` or in the JSON encoding of its result, and
  `runtime.Goexit` in `fn`, such as from a failing `Must` helper, are recovered.
  The page's promise rejects with the fixed message `exposed function failed`,
  without details, and later calls are still handled. `onError` receives a
  `*TryError` holding the panic value, `ErrExposedFunctionExited`, or an error
  wrapping the encoding error. A panic in `onError` is not recovered.
- **Error text.** The text of an error that `fn` returns, or of an encoding
  error, is the rejection message that the page receives. Do not return errors
  whose text must stay private.

`Page.Expose` answers only callback names of the form that its page function
creates, `<binding>_cb<number>`, so a reply cannot call other page globals. The
binding is itself a property of `window`: page scripts can find it and call it
directly with a name of that form, bypassing `window[name]`, and receive the
reply.

### Page scripts and Rod's JavaScript

`Page.Eval`, `Element.Eval`, element queries, and the waits and checks built on
Rod's JavaScript helpers run in the page's main world, the JavaScript context of
the page's own scripts. Only page diagnostics use an isolated world. Page
scripts can replace the built-in and DOM functions that the helpers call, such
as `querySelector`, `getBoundingClientRect`, or `MutationObserver`. A hostile
page can therefore make queries return other elements, make visibility,
stability, and interactability checks report what it chooses, and return any
value from an evaluation. Rod checks the shape of what it receives: for example,
`Page.ElementsByJS` returns `ExpectElementsError` for an array with accessor
properties, and `Element.CanvasToImage` returns an error wrapping
`ErrInvalidDataURL` for a value that is not a valid data URL. Such pages cause
errors, not panics, but the values themselves are page-controlled. Treat text,
attributes, HTML, evaluation results, canvas data, and resource content as
untrusted input.

`Page.ExposeHelpers`, a debugging aid, assigns the object that holds Rod's
helper functions to `window.rod`. Page scripts can then replace the helpers that
Rod calls in that document, so do not call it on untrusted pages.

### Event streams

`Browser.Event` and `Page.Event` preserve order within a bounded subscription:
4096 pending events and 16 MiB of protocol payload by default. `EventBuffer`
configures those limits; set it before `Browser.Connect` to cover internal
subscriptions too. The byte budget counts protocol payload, not decoded Go
objects or allocation overhead. An overflow closes the subscription and clears
its backlog. `EventWithContext` exposes `context.Cause`, which matches
`ErrEventOverflow`; event waits return that error and diagnostics return an
incomplete snapshot. Internal page-relay overflow ends that page's session.
Cancel a subscription's context when abandoning it. Applications still need
limits on the number of browser instances, pages and subscriptions they create.

### File inputs

`Page.HandleFileDialog` accepts a chooser only from the receiver's frame,
requires a backend node and recognized selection mode, and aborts on frame
navigation. Before assigning files it resolves the input to an object handle
and rechecks the frame's document identity, so a chooser queued before a
navigation cannot supply files to a replacement document.
`HandleFileDialogWithOptions` additionally pins the backend node
and mode. Use `Element.SetFiles` for a known input: it avoids a race between
multiple inputs in one frame. Once supplied, files are readable by the selected
document; do not give untrusted documents host secrets.

### Downloads

`Browser.WaitDownload(dir, options...)` defaults to 64 MiB per accepted download
and a two-minute deadline. `DownloadOptions` can restrict frame, URL, suggested
filename and a metadata predicate. `Page.WaitDownload` always restricts to its
receiver frame. Nonmatching and additional downloads observed before completion
are canceled. GUIDs must be single local filenames;
invalid values return `ErrDownloadGUID` without escaping the directory.

Chrome's download behavior remains context-wide and its progress reports are
asynchronous, so cancellation is not a filesystem quota. Completed or late
additional files can remain in caller-owned directories. `WaitDownload` does
not delete client files: its directory belongs to the browser's host, which
may be remote. Use a dedicated
incognito context and a private directory for hostile workflows, and apply OS
disk quotas where required. URL and filename metadata alone do not authenticate
content. An unfiltered browser wait still selects the first download in its
context; use page-scoped waits and provenance filters to prevent substitution.

`MustWaitDownload` uses a private temporary directory and bounds its read to
64 MiB. `MustWaitDownloadWithInfo` and `Page.MustWaitDownload` also return the
browser's provenance metadata and accept an explicit positive byte limit.
The temporary directory is removed after reading, or on failure or timeout even
when the returned wait is not called. A completed file remains until the
returned function is called. Streaming
callers can use `WaitDownload` and open the result themselves with a byte limit.

### Screenshots

A page controls its own size.

| Protection | Default | Opt-out | Risk when changed |
| --- | --- | --- | --- |
| Scroll screenshot height | `Page.ScrollScreenshot` returns an error matching `ErrScrollScreenshotTooTall`, before capturing, when the page content is taller than `ScrollScreenshotOptions.MaxHeight`, 32768 CSS pixels by default. | A larger `MaxHeight`, within the independent dimension, pixel and byte limits below. | The page chooses its content height, and stitching holds about four bytes of memory per device pixel of the result. |
| Stitched image size | `utils.SplicePngVertical`, which stitches scroll screenshots, reads image sizes from their headers and rejects a source image or a result larger than `utils.MaxSplicePixels` (2^28 pixels) before decoding pixels. | None. | — |

Full-page, scroll and clipped screenshots, including element screenshots, reject
invalid dimensions, dimensions above 32768 device pixels, and areas above
`MaxScreenshotPixels` (32 Mi pixels). Encoded images are limited to
`MaxCaptureBytes` (64 MiB); scroll captures also bound the total encoded bytes
of their tiles. PNG/JPEG dimensions are checked on return.
Failures match `ErrScreenshotTooLarge` or `ErrCaptureTooLarge`. Raw protocol
commands bypass these application-level limits.

`Page.PDF` streams through a `StreamReader` whose `MaxBytes` defaults to 64 MiB.
`MustPDF` reads that bounded stream and closes it. Streaming callers can change
`MaxBytes`; zero removes the limit. Always close streams when finished.

### Launcher defaults

**Profiles.** `launcher.New` selects a profile path `rod-profile-<random>`
directly inside `os.TempDir()`. Launch creates it, fails if anything, including
a symlink, already exists at that path, and removes the profile after the
browser exits. On Unix, the directory has mode `0700`. On Windows, Go ignores
the mode, and the directory inherits the permissions of its parent. This relies
on the parent directory being private, or on Unix sticky, as `/tmp` is; set
`TMPDIR` on Unix, or `TMP` on Windows, only to such a directory. On Linux, the
launcher's supervisor also gives each browser a private temporary directory
beneath that parent and removes it afterwards, and it stops the browser and
removes the profile when the program exits or crashes first. Other systems use
direct launches: the program removes the profile after the browser exits, so if
the program ends first, the browser can keep running with its DevTools port
open, and the profile remains.
`Launcher.UserDataDir(dir)` and `-rod=dir=...` select a caller-owned profile
instead, which Rod never removes and does not make private. It holds cookies and
other session state, so create it yourself with owner-only permissions.

**Browser isolation.** `launcher.New` preserves Chrome's sandbox, site
isolation, popup blocking and IPC flooding protection. It does not disable the
sandbox merely because it detects a container. `NoSandbox(true)` is an explicit
opt-out for environments that cannot provide Chrome's sandbox; a compromised
renderer then has the browser user's privileges. Raw `Set`/`Delete` options can
also weaken protections. `Launcher.FormatArgs` shows the selected flags.

**DevTools port.** The browser's DevTools endpoint listens on `127.0.0.1` at a
port that the browser picks (`--remote-debugging-port=0`). Chrome does not
authenticate DevTools clients, so any local process that finds the port,
including the processes of other users, can control the browser. Rod provides
no way to restrict this endpoint; where other users of the machine are not
trusted, run the browser in its own container, virtual machine, or network
namespace.

**Fixed ports.** `Launcher.Launch`, `LaunchNew` and browser launch helpers
never adopt an existing listener. An occupied fixed port returns
`launcher.ErrDebuggingPortInUse`. `NewUserMode` also requests a random port.
Attach to a known browser explicitly with `ControlURL` or `Client`; callers
must establish that endpoint's identity. TCP DevTools remains unauthenticated,
so random ports do not protect it from other users on the same host.

**Certificate errors.** The browser rejects invalid certificates by default.
`Browser.IgnoreCertErrors(true)` makes the whole browser ignore certificate
errors, which lets anyone on the network path impersonate any site.
`Launcher.IgnoreCerts(keys)` makes it accept any certificate chain that contains
one of the listed public keys, for any host name, so whoever holds a listed
private key can impersonate any site.

### Remote manager

- `rod-manager` requires its token in `ROD_MANAGER_TOKEN`, removes the variable
  from its own environment after reading it, and removes it from every browser's
  environment. It listens on `127.0.0.1:7317` by default and serves plain HTTP.
  `-tls-cert` and `-tls-key` enable authenticated HTTPS directly. Without TLS,
  a non-loopback `-address` requires `-allow-plaintext-remote`; use a TLS reverse
  proxy in front of that listener.
- `launcher.NewManager(token)` rejects every request when the token is empty.
  Requests must carry `Authorization: Bearer <token>`. The manager compares
  SHA-256 digests of the tokens in constant time and removes the header before
  its hooks run and before it proxies the connection to the browser.
- `launcher.NewManaged(ctx, serviceURL, token, options...)` requires `https://`
  or `wss://`, including on loopback. Plaintext returns
  `launcher.ErrManagerInsecureTransport` before sending credentials. A private
  CA can be supplied through `ManagerClientOptions.TLSConfig`; disabling
  certificate verification is rejected. The same TLS configuration protects
  both settings and `Client`/`MustClient` WebSocket connections. Custom clients
  using `ClientHeader` must apply the same TLS trust configuration. The token
  is sent on the settings request and WebSocket handshake, does not follow redirects, and rejects settings responses
  larger than 1 MiB. The launcher it returns holds the manager's settings,
  including the executable, environment, working directory, and switches for
  the manager's host. Use it only with `Launcher.Client` or `MustClient`:
  `Launch` and `LaunchNew` return `launcher.ErrManagedLaunch`, and `MustLaunch`
  panics with it, so a manager cannot choose a program that runs on the client.
- A token holder has full DevTools access to the browsers it launches, and
  managed clients cannot change the executable, environment, working
  directory, profile path, or debugging port. As defense in depth, the manager
  also rejects option names that are not canonical switch names, positional
  arguments that Chromium would parse as switches, and switches that load host
  programs, write host files, expose DevTools outside the manager, or delegate
  host credentials. Switches that weaken the sandbox and switches that read host
  files remain allowed. Apply a stricter policy in `Manager.BeforeLaunch`,
  which runs after this validation; the manager reasserts its profile and port
  afterwards. See [remote manager security](../lib/launcher/README.md#remote-manager-security).

### Command-line options

`rod.New`, `launcher.New`, `cdp.New`, and `launcher.NewManager` call
`defaults.Load`, which applies the `-rod` command-line flag. Several of its
options change security-relevant defaults: `bin` selects the executable that Rod
starts, `url` the endpoint that `Browser.Connect` uses, `proxy` the browser's
proxy, `dir` the profile, `port` a fixed debugging port, `monitor` a monitor
listener, and `trace` and `cdp` enable logging.

`-rod` is read only from the leading flags. Reading stops at `--`, at the first
argument that is not a flag, and at an undefined flag without `=value`; undefined
`test.*` flags are skipped. The value of another flag is never read as `-rod`.
After `flag.CommandLine` is parsed, only the parsed value of a defined `-rod`
flag is used. Pass untrusted arguments after `--` or after a positional
argument, never among the leading flags. The `DISABLE_ROD_FLAG` environment
variable, set to any value, disables `-rod`.

Credential transitions disable ambient `-rod` processing and reject Unix
launches with `launcher.ErrSecureExecution`. Linux checks `AT_SECURE`, including
file capabilities; macOS and BSD systems use `issetugid`. Linux guardians also
verify inherited socket peer credentials and that the parent runs the same
executable, then require a fresh challenge response carrying that parent PID's
kernel message credentials. No command or cleanup runs before authentication.
Profile cleanup stays anchored to an already-open parent directory. Other Unix
systems disable private re-execution and directly launch a browser process
group; application-crash cleanup is unavailable there. Run browser automation
without setuid, setgid, or file-capability transitions.

`Launcher.StartURL` and positional launch arguments must be absolute URLs with
supported schemes. Switch-shaped, relative and malformed values fail with
`launcher.ErrInvalidURL` before starting a process. `launcher.Open` returns the
validation or launch error. Use `Launcher.Set` for intentional browser switches.

### Monitor

- `Browser.ServeMonitor(addr)`, `Browser.Monitor(addr)`, and
  `-rod=monitor[=addr]` start the monitor. `ServeMonitor("")` and
  `-rod=monitor` without a value listen on `127.0.0.1` at a random port; an
  address such as `:9273` listens on every interface.
- The monitor's URL is `http://<listener>/<token>/`, with a new token of at
  least 128 random bits for each call. Requests outside the token path get
  404 Not Found; the token is compared in constant time.
- The `Host` header must be `localhost`, a loopback IP address, the listener
  address, or the local address and port that the connection reached. Other
  requests, including those that use DNS names of the machine, get
  403 Forbidden, which blocks DNS rebinding.
- The monitor serves plain HTTP, and the token is its only credential: anyone
  who has the URL can list the pages with their titles and URLs and take
  screenshots of them. Keep it on loopback or behind a trusted authenticated
  proxy.
- With `Browser.Monitor` or `-rod=monitor`, `Browser.Connect` logs
  `[monitor] <URL>` through `Browser.Logger`, even when tracing is off, and
  then opens the URL in the system browser by passing it on the command line,
  which other local users may be able to read. On shared machines, call
  `Browser.ServeMonitor` and open the URL yourself.

The monitor bounds request reads (headers and body) to five seconds, idle
keep-alive connections to 30 seconds, and request headers to 32 KiB. The read
limits apply regardless of token and Host validation, including when Go drains
the unread body of a rejected request. Screenshot processing is independent of
these read deadlines. Use a proxy or OS connection limits when exposing the
monitor to a larger network.

### Trace mode

`Browser.Trace(true)` and `-rod=trace` log actions through `Browser.Logger` and
draw overlays in pages.

- **Redacted:** text inserted by `Page.InsertText` and `Element.Input`, logged
  as `insert text (N characters redacted)`; keys that type a character or a
  space, through `Keyboard.Press`, `Keyboard.Type`, `KeyActions`, and
  `Element.Type`, logged as `press character key (redacted)` and
  `release character key (redacted)`; and the values of `Element.InputTime` and
  `Element.InputColor`. The length of inserted text remains visible.
- **Not redacted:** selectors, and query scripts with their arguments;
  `Element.Select` selectors; the `Element.SelectText` pattern; `Element.SetFiles`
  paths; the URL patterns of `Page.WaitRequestIdle` and, logged every second
  while it waits, the full URLs of the requests it waits for; and element
  descriptions. The page controls the request URLs, which can carry tokens in
  query strings, and the element descriptions.
- **Overlays:** `Page.Overlay`, `Element.Overlay`, and trace overlays show their
  message as plain text and never parse it as HTML. They are elements in the
  page's DOM: element overlays in the element's document, and page overlays,
  including traces of keyboard and text input into a same-process iframe, in
  the top-level document. Scripts of that document can read them, and
  screenshots include them.

### Diagnostics and logs

These outputs can contain sensitive or page-controlled data. Protect them like
the data the automation handles, and escape them before displaying them as
HTML.

| Output | Enabled by | Contents |
| --- | --- | --- |
| CDP log | `cdp.Client.Logger`, or `-rod=cdp`, which writes to the standard `log` package's output | Every protocol request, response, and event, unredacted: evaluated code and results, inserted text, cookies, credentials that `HandleAuth` sends with `Fetch.continueWithAuth`, fulfilled response bodies, and page content. |
| `Browser.Logger` (`DefaultLogger` writes to standard output) | Trace mode, the monitor | Trace lines as described above, including page request URLs, and the monitor URL with its token. |
| Browser output | `Launcher.Logger`; `Launcher.Output` keeps the last 64 KiB by default | The browser's standard output and error, including its DevTools WebSocket URL. Startup errors from `Browser.Launch` and `Launcher.Launch` include recent output. |
| Page diagnostics | `Page.StartDiagnostics` | Console text, exception text, and resource URLs, which can carry tokens in query strings. `DiagnosticsOptions` bounds them: 1000 records of each kind and 4096 bytes per text field by default. |
| Errors | — | `*EvalError` holds the page's exception details, `*TryError` a panic value and stack, and hijack URL errors the request URL. |

## Deployment guidance

### Scraping untrusted sites

- Keep the defaults: strict decoding, `FollowRedirects` off, the response
  limits, failing unparsable URLs, and scoped `HandleAuth` credentials.
- Keep the default site isolation and sandbox. Use a container that supports
  Chrome's sandbox.
- Prefer `ContinueRequest` when browser networking suffices. Otherwise use
  `LoadResponse(nil, ...)`, add deployment-specific `DenyNetworks`, and lower
  response byte limits as appropriate. Reserve `LoadResponseUnsafe` for trusted
  requests with a separately enforced network policy.
- Enforce request policies with `Browser.HijackRequests` rather than
  `Page.HijackRequests`, and block WebSocket endpoints outside Rod; see
  [what routes do not see](#what-routes-do-not-see).
- Set `HijackRouter.OnError` to observe blocked and failed requests.
- Bound every operation with `Page.Timeout` or a context deadline. Queries and
  waits retry until their context ends, so a page that never renders an
  element, never finishes loading, or keeps changing its DOM stalls them
  indefinitely.
- Keep untrusted sites apart from logged-in sessions, for example in a separate
  profile or a `Browser.Incognito` context.
- Treat everything read from pages, and every call of an exposed function, as
  untrusted input. Do not enable trace mode or CDP logging where the logs are
  not protected.

### Shared machines

- Other local users can connect to the DevTools port. Run the browser in its own
  container, virtual machine, or network namespace when they are not trusted.
- Keep generated profiles, or keep a caller-owned `UserDataDir` private, and
  keep `TMPDIR` on Unix, or `TMP` on Windows, pointing to a private directory,
  or on Unix a sticky one. Outside Linux, stop the browser before the program
  exits; see [profiles](#launcher-defaults).
- Keep the debugging port at 0. Launch helpers reject occupied fixed ports;
  explicit attachments must use a trusted endpoint.
- Save downloads with `MustWaitDownload` or into a directory from
  `os.MkdirTemp`.
- Capture helpers create files exclusively with mode `0600` and missing
  directories with mode `0700` on Unix. Existing files, symlinks, FIFOs and
  directories fail instead of being overwritten. Windows inherits parent ACLs.
  Keep existing parent paths trusted: directory creation and initial parent
  resolution can traverse existing symlinks. The final file is opened relative
  to an anchored directory and never follows a destination symlink. An empty
  name still selects `tmp/screenshots` or `tmp/pdf`; returned bytes can be
  written separately when an application intentionally needs overwrite.
- Serve the monitor with `Browser.ServeMonitor` on loopback, and open its URL
  yourself instead of using `Browser.Monitor` or `-rod=monitor`.
- Pass untrusted command-line arguments after `--`, or set `DISABLE_ROD_FLAG`.

### Exposing the manager

- Use a long random token, and give it only to those you would let administer
  the browser host.
- Serve TLS with `rod-manager -tls-cert=... -tls-key=...`, or keep its loopback
  listener behind a TLS reverse proxy, and connect with `https://` or `wss://`. Use
  `-allow-plaintext-remote` only when the plaintext hop between the TLS
  terminator and the manager stays on a trusted network.
- Run the manager on a host or container dedicated to it, and enforce stricter
  launch policy in `Manager.BeforeLaunch`.
