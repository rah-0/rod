# Security

This document describes what Rod protects against, the protections that are on
by default, and the options that turn them off. Every opt-out is explicit and
named below, together with the risk it brings; a program that uses one accepts
that risk. For how these defaults changed from earlier versions, see
[Breaking changes](BREAKING.md#v01250--compared-with-v01240).

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

A malicious endpoint still controls what the browser reports. Well-formed but
false data passes every check: validation prevents crashes and unbounded memory
use, not wrong results.

### What Rod does not defend against

- **The automation code itself**: what it does with page data, what exposed
  functions and hijack handlers do, and the opt-outs it enables.
- **The browser's own security**: the renderer sandbox, site isolation, the
  same-origin policy, and browser bugs. Rod relies on the browser to contain
  page content, and its default launch flags disable some of these protections;
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
| Redirects of responses loaded by Go | A 3xx response and its `Location` header return to the browser, which follows the redirect under its own CORS and private network policies. Routes that match the next URL intercept it again. | Set `Hijack.FollowRedirects = true` in the handler before loading the response. It applies to that request only. | Go follows redirects with the client's `CheckRedirect`, or with `http.Client`'s default policy of at most 10 redirects when the client has none, and the final response fulfills the original URL. A page whose request is redirected, by its own server or an open redirect, reads the response of any URL the Go client can reach, including internal services, as same-origin data. The final response's headers, such as `Set-Cookie`, `Content-Security-Policy`, and CORS headers, apply to the original URL. Go forwards custom request headers, such as API tokens, to every redirect target; it drops `Authorization`, `Cookie`, and its other credential headers only when the target host is neither the original host nor one of its subdomains, so they still reach other ports and schemes of the same host, including plain `http://`. Go also sends a `Referer`: the original request's `Referer` unchanged or, when it has none, the previous URL with its path and query, except on a redirect from `https` to `http`. Restrict redirect targets with the client's `CheckRedirect`, and remove the headers they must not receive there. |
| Response body size | `Hijack.MaxResponseBodyBytes` starts at `DefaultMaxResponseBodyBytes` (64 MiB); zero or a negative value selects that default. A larger `Content-Length` fails before reading; otherwise reading stops one byte past the limit. The error matches `ErrResponseBodyTooLarge`, and `Hijack.Response` stays unchanged. | Raise `MaxResponseBodyBytes` in the handler; `math.MaxInt64` removes the limit. | Each concurrent `LoadResponse` buffers up to the limit. Fulfilling the request holds two more copies of the body, base64 encoded, together about 2.7 times its size: the protocol message and its WebSocket frame. Chrome closes the whole DevTools connection when it receives a message larger than about 100 MiB, which a body above about 74 MiB, or a smaller body with large headers, produces. |
| Response header size | `Hijack.MaxResponseHeaderBytes` starts at `DefaultMaxResponseHeaderBytes` (256 KiB, counted as an HTTP/1.1 header block), the size at which Chrome rejects response headers from the network. Larger headers return an error matching `ErrResponseHeadersTooLarge`, and `Hijack.Response` stays unchanged. The client's transport applies its own limit first; `http.Transport` allows 10 MiB by default. | Raise `MaxResponseHeaderBytes` in the handler. | More memory per request, and the page receives headers that Chrome rejects from the network. The headers and the base64 encoded body share Chrome's message limit. |
| Unparsable request URLs | A paused request whose URL `net/url` cannot parse, such as a path with `%zz` or a stray `%` as in `/sale-50%-off`, or a host name containing `{`, fails with `net::ERR_FAILED`. No handler runs, and the parse error goes to the router's `OnError`. `HijackRequest.URL` is therefore never nil. | `HijackRouter.ContinueUnparsableURLs(true)` continues such requests unmodified. Handlers still do not run, and the parse error is still reported. | A page can evade any route by adding an invalid escape, or such a host name, to a URL that still matches the route's pattern: the request reaches the network without the blocking, mocking, or rewriting that the route applies. Enable it only when no route enforces a policy that the page must not evade. |
| Handler panics and `runtime.Goexit` | Always recovered, including panics from `Must` helpers such as `MustLoadResponse`. Unless the handler had started resolving the request, the router fails it with `net::ERR_FAILED`; each paused request is resolved once. `Hijack.OnError` receives a `*TryError`, which unwraps to the panic value, or `ErrHijackHandlerExited`. | To stop the process, re-panic from `OnError`. A panic in `OnError` is not recovered; the request is already resolved by then. | The process ends on a handler failure that a page can trigger. |

By default, the router discards errors, so blocked, failed, and oversized
requests are silent. `HijackRouter.OnError(fn)` sets a router-wide handler: it
is the initial `Hijack.OnError` of each later request and also receives errors
for requests that the router resolves without running handlers. It is called
concurrently from the goroutines that serve requests.

**Requests that Go loads bypass the browser's network policy.** In both redirect
modes, `LoadResponse` sends the request with the client's transport, proxy, and
TLS settings. `MustLoadResponse` and a nil client use `http.DefaultClient`,
which reads `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY`, verifies certificates
with the system roots, and has no timeout. The request does not use the
browser's proxy configuration, such as `--proxy-server`, its private network
access checks, or its certificate-error settings. Go also resolves host names
itself, so DNS rebinding to an internal address can give a page a same-origin
read without any redirect. When pages are untrusted, give the client a
`Transport` whose `Proxy` is nil and whose dialer, through `net.Dialer.Control`,
rejects every address that is not global unicast or is private: with
`netip.Addr`, reject when `!ip.IsGlobalUnicast() || ip.IsPrivate()`. This rule
also rejects the unspecified addresses `0.0.0.0` and `::`, which reach the local
host on Linux, and loopback, link-local, and multicast addresses. Reject other
internal ranges of your network too, such as the shared address space
`100.64.0.0/10`. `Control` sees the resolved address, while `CheckRedirect` sees
only the URL. Through a proxy, the dialer sees only the proxy's address, and the
proxy decides which targets are reachable. Alternatively, use
`Hijack.ContinueRequest`, which leaves the request to the browser.

**Each paused request costs a goroutine.** The router serves every paused
request on its own goroutine, without a concurrency limit. A page that issues
many parallel requests to routes that call `LoadResponse` holds one goroutine
and up to `MaxResponseBodyBytes`, plus the copies made while fulfilling, per
request. Lower `MaxResponseBodyBytes`, continue large resources with
`ContinueRequest`, or bound concurrency in the handler, for example with a
semaphore. A request that Go loads lasts until the client's `Timeout` or the
request's context ends, and the request's context lasts as long as the router;
set a client `Timeout`, or call `HijackRequest.SetContext` with a deadline.

#### What routes do not see

`Page.HijackRequests` intercepts only the requests of its page's target. It does
not see requests from the windows that the page opens, which Rod's default
`--disable-popup-blocking` lets it open without a user gesture, or from service
workers. With site isolation enabled, it also misses cross-site iframes, which
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

Rod accepts plain `ws://` and `http://` endpoints, including remote ones; only
`launcher.NewManaged` rejects them for non-loopback hosts. Anyone who can read
or change such a connection sees all protocol traffic and can control the
browser. Reach remote browsers through `wss://` or an encrypted tunnel.

### Exposed functions

`Page.Expose(name, fn, onError)` and `Page.MustExpose` install
`window[name]`, which calls `fn` in Go with a JSON argument from the page.

- **Reach.** Rod adds the binding with `Runtime.addBinding` and installs
  `window[name]` in every new document of the page's target with
  `Page.addScriptToEvaluateOnNewDocument`, so frames other than the main frame
  can call it. In Chrome 152 with Rod's default launch flags, which disable site
  isolation, a cross-site iframe called `fn` and received its result. With site
  isolation enabled, a cross-site iframe could not, but an iframe of the same
  site on another origin, such as another port, still could. Any script in
  those frames, including third-party scripts, can call `fn` with any JSON
  value, so validate the argument before acting on it.
- **Call rate and memory.** `fn` handles one call at a time, in the order the
  page made the calls. Calls that `fn` has not handled and replies that the
  page has not received, including the encoded results of `fn`, stay in the Go
  process's memory without a limit, so a page that calls faster than it receives
  replies increases memory use. Keep `fn` fast, and call the returned stop
  function when a page misbehaves.
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

`Browser.Event` and `Page.Event` are ordered, lossless, and unbounded: events
that have not been read stay in memory, and a page decides how many events it
causes, such as console messages. Keep reading, or cancel the context, to
release a subscription. `EachEvent`, `WaitEvent`, and `WaitDownload` keep the
events they match from the moment they subscribe until their wait runs or their
context ends.

### Downloads

- `Browser.WaitDownload(dir)` saves the download as
  `filepath.Join(dir, info.GUID)`, a name that the browser chooses. The wait
  returns an error matching `ErrDownloadGUID`, and no download, when the GUID is
  not a single local file name: empty, `.`, `..`, containing `/`, `\`, `:`, or a
  NUL byte, or rejected by `filepath.IsLocal`. The path therefore stays inside
  `dir`. Use a `dir` that other local users cannot write, such as one from
  `os.MkdirTemp`.
- From the call to `WaitDownload` until its wait returns or its context ends,
  the browser saves every download of that browser context into `dir` without a
  prompt, from any page. The wait returns only the first download; the others
  remain in `dir`.
- `Browser.MustWaitDownload` creates a new directory with `os.MkdirTemp` for
  each call, with mode `0700` on Unix; on Windows it inherits the permissions of
  `os.TempDir()`. It removes the directory after the returned function reads the
  file, and also when setup, the wait, or the read fails. The directory remains
  if the returned function is never called.
- `MustWaitDownload` reads the whole file into memory, so the page decides how
  much memory it uses. For untrusted pages, call `WaitDownload` and read the
  file with a size limit.
- Downloaded bytes are page content.

### Screenshots

A page controls its own size.

| Protection | Default | Opt-out | Risk when changed |
| --- | --- | --- | --- |
| Scroll screenshot height | `Page.ScrollScreenshot` returns an error matching `ErrScrollScreenshotTooTall`, before capturing, when the page content is taller than `ScrollScreenshotOptions.MaxHeight`, 32768 CSS pixels by default. | A larger `MaxHeight`. | The page chooses its content height, and stitching holds about four bytes of memory per device pixel of the result. |
| Stitched image size | `utils.SplicePngVertical`, which stitches scroll screenshots, reads image sizes from their headers and rejects a source image or a result larger than `utils.MaxSplicePixels` (2^28 pixels) before decoding pixels. | None. | — |

`Page.Screenshot(true, …)` and `Page.MustScreenshotFullPage` resize the viewport
to the page's content size without a Rod limit; `cdp.WebSocket.MaxMessageSize`
bounds the image that the browser returns.

### Launcher defaults

**Profiles.** `launcher.New` selects a profile path `rod-profile-<random>`
directly inside `os.TempDir()`. Launch creates it, fails if anything, including
a symlink, already exists at that path, and removes the profile after the
browser exits. On Unix, the directory has mode `0700`. On Windows, Go ignores
the mode, and the directory inherits the permissions of its parent. This relies
on the parent directory being private, or on Unix sticky, as `/tmp` is; set
`TMPDIR` on Unix, or `TMP` on Windows, only to such a directory. On Unix, the
launcher's supervisor also gives each browser a private temporary directory
beneath that parent and removes it afterwards, and it stops the browser and
removes the profile when the program exits or crashes first. Windows has no
supervisor: the program removes the profile after the browser exits, so if the
program ends first, the browser keeps running with its DevTools port open, and
the profile remains.
`Launcher.UserDataDir(dir)` and `-rod=dir=...` select a caller-owned profile
instead, which Rod never removes and does not make private. It holds cookies and
other session state, so create it yourself with owner-only permissions.

**Default flags that weaken isolation.** `Launcher.FormatArgs` lists every flag
that a launch uses.

| Default | Effect | To remove |
| --- | --- | --- |
| `--disable-features=site-per-process,TranslateUI` and `--disable-site-isolation-trials` | Site isolation is off, so pages and iframes of different sites can share a renderer process. A compromised renderer or a side channel in one site can then reach data of the others, and cross-site iframes can call [exposed functions](#exposed-functions). | `l.Delete("disable-site-isolation-trials").Set("disable-features", "TranslateUI")` |
| `--no-sandbox`, added when Rod detects a container through `/.dockerenv`, `/.containerenv`, or the `KUBERNETES_SERVICE_HOST` environment variable | Chrome's sandbox is off, so a compromised renderer runs with the privileges of the browser's user. | `l.NoSandbox(false)`, in a container that supports Chrome's sandbox. Chrome may refuse to run as root without `--no-sandbox`. |
| `--disable-popup-blocking` | Pages can open windows without a user gesture. Each window is a separate target, which the page's `Page.HijackRequests` router and diagnostics do not cover; see [what routes do not see](#what-routes-do-not-see). | `l.Delete("disable-popup-blocking")` |
| `--disable-ipc-flooding-protection` | Chrome no longer throttles pages that flood the browser process with messages, such as rapid navigation and history API calls. | `l.Delete("disable-ipc-flooding-protection")` |
| Other flags, such as `--disable-client-side-phishing-detection` | They turn off browser features, such as Chrome's client-side phishing detection, that are not isolation boundaries. | `l.Delete("<name>")` |

**DevTools port.** The browser's DevTools endpoint listens on `127.0.0.1` at a
port that the browser picks (`--remote-debugging-port=0`). Chrome does not
authenticate DevTools clients, so any local process that finds the port,
including the processes of other users, can control the browser. Rod provides
no way to restrict this endpoint; where other users of the machine are not
trusted, run the browser in its own container, virtual machine, or network
namespace.

**Fixed ports.** `Launcher.RemoteDebuggingPort(port)` and `-rod=port=...`
select a fixed port. `Launcher.Launch`, which `Browser.Connect` also uses when it
launches a browser, first connects to whatever already answers at
`127.0.0.1:<port>`, which can be a browser or server that another user started.
`launcher.NewUserMode` uses port 37712 this way. `Browser.Launch` and
`Launcher.LaunchNew` never adopt an existing endpoint: an occupied port returns
an error matching `launcher.ErrDebuggingPortInUse`.

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
  A non-loopback `-address` requires `-allow-plaintext-remote`; terminate TLS in
  a reverse proxy or tunnel in front of the manager.
- `launcher.NewManager(token)` rejects every request when the token is empty.
  Requests must carry `Authorization: Bearer <token>`. The manager compares
  SHA-256 digests of the tokens in constant time and removes the header before
  its hooks run and before it proxies the connection to the browser.
- `launcher.NewManaged(ctx, serviceURL, token)` rejects `http://` and `ws://`
  URLs whose host is not loopback with `launcher.ErrManagerInsecureTransport`;
  there is no opt-out. It sends the token on the settings request and on the
  WebSocket handshake, does not follow redirects, and rejects settings responses
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
- Remove the flags that disable site isolation, and keep Chrome's sandbox. In a
  container, provide one that supports the sandbox and call `NoSandbox(false)`.
- Prefer `ContinueRequest` to `LoadResponse`. When Go must load responses, use a
  client without a proxy whose dialer rejects addresses that are not global
  unicast or are private, as described in
  [request hijacking](#request-hijacking), give it a `Timeout`, lower
  `MaxResponseBodyBytes`, and bound concurrent handlers.
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
  or on Unix a sticky one. On Windows, stop the browser before the program
  exits; see [profiles](#launcher-defaults).
- Launch with `Browser.Launch` or `Launcher.LaunchNew`, or keep the debugging
  port at 0, so that Rod never adopts another user's endpoint.
- Save downloads with `MustWaitDownload` or into a directory from
  `os.MkdirTemp`.
- When given a file name, `Page.MustScreenshot`, `Page.MustScreenshotFullPage`,
  `Page.MustScrollScreenshot`, `Page.MustPDF`, and `Element.MustScreenshot` save
  the capture through `utils.OutputFile`, which creates missing directories with
  mode `0775` and files with mode `0664` before the umask, so other users can
  usually read them. An empty name saves under `tmp/screenshots` or `tmp/pdf` in
  the working directory. Save captures of sensitive pages into a private
  directory, or write the returned bytes yourself.
- Serve the monitor with `Browser.ServeMonitor` on loopback, and open its URL
  yourself instead of using `Browser.Monitor` or `-rod=monitor`.
- Pass untrusted command-line arguments after `--`, or set `DISABLE_ROD_FLAG`.

### Exposing the manager

- Use a long random token, and give it only to those you would let administer
  the browser host.
- Keep `rod-manager` on its loopback address behind a TLS reverse proxy or an
  encrypted tunnel, and connect with `https://` or `wss://` URLs. Use
  `-allow-plaintext-remote` only when the plaintext hop between the TLS
  terminator and the manager stays on a trusted network.
- Run the manager on a host or container dedicated to it, and enforce stricter
  launch policy in `Manager.BeforeLaunch`.
