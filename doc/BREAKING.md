# Breaking changes

Incompatible APIs, behavior, and development workflows are recorded here, newest
version first. Each entry compares that version with its previous release and
names the affected functions, types, fields, or commands with their migration.
When skipping releases, apply every intervening entry, oldest to newest.

Group changes under the version that introduces them, above earlier versions.
Each heading names that version and the previous version it changes. Describe
the implementation for the named version. Keep historical entries intact; add
later changes under their own version.

## v0.125.0 — compared with v0.124.0

[Security](SECURITY.md) describes the threat model, the secure defaults of this
version, and their opt-outs.

### Request hijacking

| Affected | Change | Migration |
| --- | --- | --- |
| `Hijack.LoadResponse`, `Hijack.MustLoadResponse` | By default, Go no longer follows redirects. A 3xx response and its `Location` header return to the browser, which follows the redirect under its own CORS and network policies; routes matching the next URL intercept it again. The client's `CheckRedirect` is not used, and `Hijack.Response.RawResponse` holds the 3xx response. The new `Hijack.FollowRedirects` field, initially false, makes Go follow redirects with the client's `CheckRedirect`, or with `http.Client`'s default policy of at most 10 redirects when the client has none; the final response then fulfills the original request. A nil client uses `http.DefaultClient`. | Expect a 3xx `ResponseCode` for redirecting URLs. Handle the redirect target in the route that matches it instead of expecting the final body from the first request. To keep the previous behavior for a route, set `FollowRedirects` in its handler before loading the response. The page can then read the response of any URL that a redirect names and the client can reach, including internal services, as same-origin data. The final response's headers, including `Set-Cookie`, apply to the original URL. Go forwards custom request headers to every redirect target, and `Authorization` and `Cookie` to subdomains and to other ports and schemes of the same host. It also sends the original request's `Referer` or, when that request has none, the previous URL as the `Referer`, except from `https` to `http`. Restrict redirect targets, and remove headers, with the client's `CheckRedirect`. |
| `Hijack.LoadResponse` body size | Bodies are limited by the new `Hijack.MaxResponseBodyBytes` field, initially `DefaultMaxResponseBodyBytes` (64 MiB). A larger body returns an error matching `ErrResponseBodyTooLarge`. Responses to HEAD requests and 1xx, 204, and 304 responses load an empty body whatever their `Content-Length`; an HTTP/2 204 or 304 response with a `Content-Length` header no longer fails with `unexpected EOF`. Status, headers, and body are applied only after the whole response loads; an error leaves `Hijack.Response` unchanged. | Raise `MaxResponseBodyBytes` in the handler before calling `LoadResponse` for larger resources, or continue those requests with `ContinueRequest`. Chrome closes the DevTools connection when a fulfill message exceeds 100 MiB. Because of base64 encoding, that happens for bodies above about 74 MiB with small headers, and for smaller bodies with large headers. |
| `Hijack.LoadResponse` headers | Response headers larger than the new `Hijack.MaxResponseHeaderBytes` field, initially `DefaultMaxResponseHeaderBytes` (256 KiB, counted as an HTTP/1.1 header block, the size at which Chrome rejects network responses), return an error matching the new `ErrResponseHeadersTooLarge`, and `Hijack.Response` stays unchanged. Zero or a negative value selects the default. Headers are applied in one pass instead of one scan per header name. | Raise `MaxResponseHeaderBytes` in the handler before calling `LoadResponse` for responses with larger headers. Chrome accepts larger headers in a fulfilled response, but the headers and the base64 encoded body share Chrome's 100 MiB DevTools message limit. Alternatively, continue those requests with `ContinueRequest`. |
| `HijackRouter` handlers that panic | A handler panic or `runtime.Goexit`, including one raised by a `Must` helper, no longer terminates the process or leaves the request paused. The router fails the request with `net::ERR_FAILED` and passes a `*TryError` or `ErrHijackHandlerExited`, joined with any error failing the request, to `Hijack.OnError`. | Handle these errors in `OnError`. `errors.Is(err, &rod.TryError{})` identifies panics, and the error unwraps to the panic value. Re-panic from `OnError` if the process must stop. |
| Unparsable request URLs | A paused request whose URL `net/url` rejects, such as a path containing `%zz` or a stray `%` as in `/sale-50%-off`, or a host name containing `{`, fails with `net::ERR_FAILED` before any handler runs, and the parse error is reported to `HijackRouter.OnError`. Handlers previously received a nil `HijackRequest.URL`. The new `HijackRouter.ContinueUnparsableURLs(true)` continues such requests unmodified instead; handlers still do not run, and the parse error is still reported. | Observe these requests with `HijackRouter.OnError`. Handlers can rely on a non-nil `URL`. For sites that use such URLs, call `ContinueUnparsableURLs(true)` when no route must block, mock, or rewrite them: a page can then evade any route by adding an invalid escape to a URL. |

`Hijack.OnError` now starts as the handler registered with the new `HijackRouter.OnError`, and a nil `Hijack.OnError` also reports there. Set `HijackRouter.OnError` once to receive errors from every request.

### HTTP authentication

| Affected | Change | Migration |
| --- | --- | --- |
| `Browser.HandleAuth`, `Browser.MustHandleAuth` | Take an `AuthCredentials` value instead of a username and password. Its `Source` (`proto.FetchAuthChallengeSourceServer` or `proto.FetchAuthChallengeSourceProxy`) and `Origin` (scheme, host, and optional port) select the challenger. Only a matching challenge receives the credentials and ends the wait. Other challenges receive the browser's default response, which headless Chrome reports as `net::ERR_INVALID_AUTH_CREDENTIALS`, and the wait continues. An invalid source or origin returns an error matching `ErrAuthCredentials` without changing the Fetch configuration. With `AuthCredentials.AnyOrigin` set and `Origin` empty, the first challenge from `Source` receives the credentials whatever its origin. | Replace `HandleAuth(user, pass)` with `HandleAuth(rod.AuthCredentials{Source: proto.FetchAuthChallengeSourceProxy, Origin: "http://proxy.example:3128", Username: user, Password: pass})`. Use `proto.FetchAuthChallengeSourceServer` and the site's origin for server authentication. Cancel the wait's context when the matching challenge may not occur. When the challenger cannot be named in advance, such as a proxy that a PAC script selects, set `AnyOrigin: true` instead of `Origin`. For server challenges, any server that the browser loads a resource from during the wait can then receive the credentials. |

### Event decoding

`Message.Load` reports decoding failures as errors. A malformed or incompatible event from the browser endpoint no longer panics, including inside Rod's own goroutines.

| Affected | Change | Migration |
| --- | --- | --- |
| `rod.Message.Load` | Returns `(bool, error)` instead of `bool`. A message for another method returns `false` and a nil error. When the method matches but its parameters cannot be decoded, it returns `false` and the decoding error, and leaves the output unchanged. | Replace `if msg.Load(&event)` with `ok, err := msg.Load(&event)` and handle `err`. The new `Message.MustLoad` returns `bool` and panics on the decoding error. |
| `Browser.EachEvent`, `Page.EachEvent`, `Browser.WaitEvent`, `Page.WaitEvent`, and other waits using `rod.On` handlers | An event that cannot be decoded ends the wait, and the wait function returns the decoding error instead of panicking. | Handle the error returned by the wait function. |
| `Page.Close` | Returns the decoding error when a `Target.targetDestroyed` or `Page.javascriptDialogClosed` event cannot be decoded. | Handle the returned error. |
| `Browser.WaitDownload` | The wait returns the decoding error when a target or download event cannot be decoded, together with the already selected download, if any. | Handle the error returned by the wait function. |
| `PageDiagnostics.Stop`, `ErrDiagnosticsIncomplete` | Events that cannot be decoded are skipped. `Stop` returns the collected snapshot with an error that matches `ErrDiagnosticsIncomplete` and wraps the first decoding error. | Treat `ErrDiagnosticsIncomplete` as a partial snapshot. Use `errors.As` to inspect the decoding error. |

`Page.Expose` skips binding calls it cannot decode, and page session tracking ignores target lifecycle events it cannot decode.

### Downloads

| Affected | Change | Migration |
| --- | --- | --- |
| `Browser.WaitDownload` | The wait returns an error matching the new `ErrDownloadGUID`, and no download, when the browser reports a GUID for the selected download that is not a single local file name. That covers an empty GUID, `.`, `..`, a GUID containing `/`, `\`, `:` or a NUL byte, and one that `filepath.IsLocal` rejects. `filepath.Join(dir, info.GUID)` therefore stays inside `dir`. | Handle `ErrDownloadGUID`. GUIDs that Chrome generates are unaffected. |
| `Browser.MustWaitDownload` | Each call saves the download in a new directory from `os.MkdirTemp`, with mode `0700` on Unix, instead of the shared `rod/downloads` directory under `os.TempDir()`. The directory is removed after the returned function reads the file, and also when setup, the wait, or the read fails. | Do not look for downloaded files under `os.TempDir()/rod/downloads`. Call the returned function to remove the directory; it remains if the function is never called. |

### Exposed functions

| Affected | Change | Migration |
| --- | --- | --- |
| `Page.Expose` | `fn` still handles one call at a time, in the order the page made the calls, and replies reach the page in that order. Replies are sent separately from `fn`, so `fn` can start the next call before the page receives the previous reply. Replies that wait for the page are delivered in batches, so several promises can resolve in the same task. | In page code, await an exposed call before making another call that depends on its result or side effects. Do not rely on a promise continuation running before the next reply resolves. |
| `Page.Expose`, `Page.MustExpose` | Take a third argument, `onError func(error)`, which receives the errors of calls that `fn` did not return: a `*TryError` holding the panic value when `fn` or the JSON encoding of its result panics, the new `ErrExposedFunctionExited` when `fn` ends its goroutine with `runtime.Goexit`, and an error wrapping the encoding error when a result cannot be JSON-encoded. `onError` runs on the goroutine that calls `fn`, after the call's reply is queued, so later calls wait for it. A panic in `onError` is not recovered. | Pass `nil` to discard these errors, or a function that logs or records them. `errors.Is(err, &rod.TryError{})` identifies panics. Re-panic from `onError` if the process must stop. |
| Exposed functions that panic or exit | A panic in `fn`, including one raised by a `Must` helper, no longer terminates the process. `runtime.Goexit` in `fn`, such as from a `WithPanic` fail function, no longer ends the exposure and leaves every later call unsettled. The call's promise rejects with the message `exposed function failed`, which carries no details of the failure, and later calls are handled in order. | Observe these failures through `onError`, and handle the rejection in page code. |

Calls that `fn` has not handled and replies that the page has not received, including the encoded results of `fn`, stay in the memory of the Go process until then. A page that keeps making calls faster than it receives the replies therefore increases memory use. `Page.Expose` also ignores a call of its internal binding whose callback name does not have the form that its page function creates, `<binding>_cb<number>`, so a reply cannot call other page globals. Page scripts can still call the binding directly with a name of that form.

### Required protocol fields

Command results and events are checked for every field the protocol requires before Rod or the caller uses them, and a malformed response from the browser endpoint returns an error. Before, a missing string, number, boolean, array or map decoded as its zero value, and a missing object left a nil value. Rod or the caller dereferenced that nil value later, which panicked in places such as `Page.ObjectToJSON`, `Element.ShadowRoot`, `Browser.Pages`, `Page.WaitOpen` and `Page.Reload`, or returned it without an error, as from `Page.Eval` and `Page.Info`.

The generated `Call` methods in `lib/proto`, the new `proto.Unmarshal` and `proto.Decoding.Unmarshal`, and `Message.Load` return an error in two cases, at any depth. The first is a member that the protocol requires and that is missing or `null`. The second is an array that contains `null`. `null` is accepted for `jsonvalue.Value` values, where it is a value, and for `float64` numbers, which Chrome sends as `null` when JSON cannot represent them, such as infinity; such numbers keep their zero value. The error matches the new `proto.ErrMissingField` and is a `*proto.MissingFieldError` naming the decoded type and the JSON path, for example `proto: DOMGetDocumentResult: root.children[2].nodeName is missing or null`. See [required fields](../lib/proto/README.md#required-fields).

The checks follow the protocol schema that the bindings were generated from, which `lib/proto/generate/schema-provenance.json` records. Members that this schema marks as experimental or deprecated are not required, because an older browser can lack an experimental member and a newer one can drop a deprecated member. Members that older browsers omit are not required either. `lib/proto/generate/schema-compatibility.json` lists them, and their documentation starts with `(optional in older browsers)`. The list covers Chromium 128, which, for example, sends `Debugger.scriptParsed` without `buildId` and `Network.requestWillBeSentExtraInfo` without `clientSecurityState.localNetworkAccessRequestPolicy`, and answers `Network.getRequestPostData` without `base64Encoded`. Members that are not required keep their zero value, or are nil, when they are missing. A browser or endpoint that omits any other required member fails the check, even when the caller does not read that member.

| Affected | Change | Migration |
| --- | --- | --- |
| Custom `rod.CDPClient` and `proto.Client` implementations, test doubles, proxies, non-Chrome endpoints, and browsers older than Chromium 128 or otherwise lacking a required member of the generated schema | A command result or event that lacks a required member fails to decode. For example, `{}` fails as the result of `Runtime.evaluate` or `Runtime.callFunctionOn` (for `result`), `{"result":{}}` fails without the remote object's `type`, `{"node":{}}` fails as the result of `DOM.describeNode` without `nodeId`, `backendNodeId`, `nodeType`, `nodeName`, `localName` and `nodeValue`, and `Target.getTargetInfo` fails without `targetInfo` and its `targetId`, `type`, `title`, `url` and `attached`. A `Target.getTargets` result with a `null` entry also fails. So do events such as `Target.targetCreated` without `targetInfo`, `Network.requestWillBeSent` without `request` and `initiator`, and `Network.loadingFinished` without `timestamp` and `encodedDataLength`. Go's `encoding/json` encodes nil slices and maps as `null`, so a double that encodes the generated types fails when a required slice or map, such as `NetworkRequest.Headers`, is nil. | Return complete protocol data with every required member, using empty values such as `""`, `0`, `[]` and `{}` where there is nothing to report. Use `errors.As` with `*proto.MissingFieldError` to find the type and path of the missing member. For an endpoint that does not send complete data, see `Browser.Decoding` below. |
| Rod APIs that send commands, such as `Page.Eval`, `Page.Evaluate`, `Page.ObjectToJSON`, `Page.Info`, `Element.Describe`, `Element.ShadowRoot`, `Browser.Pages`, `Page.Screenshot`, `Page.ScrollScreenshot` and `Element.Screenshot` | Return an error matching `proto.ErrMissingField`, and their `Must` helpers panic with it. Before, most of these APIs returned a nil result or zero values without an error, or panicked on the nil pointer. The screenshot methods returned an error that did not name the missing layout metric. | Handle the error. |
| `Browser.WaitDownload`, `Browser.MustWaitDownload` | A `null` entry in the `Target.getTargets` result fails setup with an error matching `proto.ErrMissingField`. A `Target.targetCreated` or `Target.targetInfoChanged` event without `targetInfo` ends the wait with that error, and so do download events without a required member. Before, Rod skipped null target records and missing target information, and the download wait continued. | Handle the error. |
| `Message.Load`, `Message.MustLoad`, `PageDiagnostics`, and waits using `rod.On` handlers, such as `Browser.EachEvent`, `Page.WaitEvent`, `Page.WaitOpen` and `Page.Reload` | An event without a required member is a decoding error. `Load` returns it wrapped, waits end with it, and diagnostics skip the event and report the error with `ErrDiagnosticsIncomplete`. | Handle it like other decoding errors; `errors.Is(err, proto.ErrMissingField)` identifies it. |
| `HijackRouter.Run` | A `Fetch.requestPaused` event without a required member, such as `request`, ends `Run` with the decoding error, and the router stops as it does after `Stop`. Handlers do not run for that event. | Handle the error returned by `Run`. |
| `Browser.HandleAuth`, `Browser.MustHandleAuth` | The wait ends with the decoding error on a `Fetch.authRequired` event without a required member, such as `request` or `authChallenge`, or on a `Fetch.requestPaused` event without one. Rod does not answer that event, so a challenge does not receive the credentials. Before, Rod continued such a paused request, and a challenge without `authChallenge` received the browser's default response. | Handle the error returned by the wait function. |
| `Page.ElementsByJS` and the element-list queries built on it | A `Runtime.getProperties` result with a `null` property descriptor, or a descriptor without `configurable` or `enumerable`, returns an error matching `proto.ErrMissingField` instead of `ExpectElementsError`. | Handle the error. |
| The generated `Call` methods in `lib/proto`, `Message.Load` and the waits using `rod.On` handlers, and Rod APIs that return command results or events | Command results, events, and the objects they contain are decoded by generated code instead of `encoding/json`. Member names must match the protocol's names exactly; `encoding/json` also matched them case-insensitively. A JSON array replaces the previous content of a slice instead of being decoded into its elements, and binary (`[]byte`) fields accept only base64 strings, not arrays of numbers. Decoding stops at the first error, so the result that a `Call` method returns with a `*json.UnmarshalTypeError` holds only the members decoded before the mismatched value; `encoding/json` also decoded the members after it. The error's `Field` holds the JSON path, with array indexes in brackets, such as `node.children[0].nodeType` instead of `node.children.0.nodeType`. Other types, such as command parameters and types of other packages that embed a generated type, are decoded with `encoding/json` and are not checked. | Send member names as the protocol spells them, and do not read a result returned with an error. Decode data that does not follow the protocol with `encoding/json`, for example the bytes that `Browser.Call` or `Page.Call` returns. |
| Endpoints that do not send complete protocol data | The new `Browser.Decoding(proto.DecodeLenient)` decodes the command results of the browser, its pages and elements, the events it receives, and the results and events that Rod decodes itself without these checks. Missing and `null` members and `null` array entries keep their zero value, which is nil for objects and arrays, so a `null` entry of an array of numbers is 0. `proto.DecodeLenient.Unmarshal` decodes other data the same way, and a `proto.Client` that implements the new `proto.Decodable` interface selects the decoding of its command results. The default is `proto.DecodeStrict`. With lenient decoding, Rod's methods check the objects, arrays and object entries that they use. Instead of panicking or returning a nil result, they return an error matching `proto.ErrMissingField`: a `*proto.MissingFieldError` that names the missing field as strict decoding does. They also treat an empty identifier, or execution context ID 0, as missing where they would use it to address a browser context, target, session, search, script or execution context, such as the session ID of `Target.attachToTarget`. They act on other missing members as zero values. The values that they return or pass on, such as the node from `Element.Describe` and the events that handlers receive, can hold nil objects and entries. | Keep the default for Chrome and Chromium-based browsers. Opt in, before `Browser.Connect`, only for an endpoint you trust to send what your code reads, and check the fields that your own code reads. `errors.Is(err, proto.ErrMissingField)` matches with both decodings. |

### Stream reads

`StreamReader` has a `ChunkSize` field. `Read` requests the larger of `ChunkSize` and its buffer length with each `IO.read` and returns data beyond the buffer from later reads. `NewStreamReader` leaves `ChunkSize` at zero, which requests the buffer length. Readers returned by `Page.PDF` use 4 MiB, so copying a PDF through small buffers, as `io.Copy` and `utils.OutputFile` do, needs one round trip per 4 MiB instead of one per buffer. Keep `ChunkSize` at zero for a response body still loading, such as a stream from `Fetch.takeResponseBodyAsStream`: the browser answers those reads only when the requested size arrives or the body ends.

| Affected | Change | Migration |
| --- | --- | --- |
| `Page.PDF`, `Page.MustPDF` | Each `IO.read` requests at least 4 MiB instead of the buffer length. A 4 MiB response is a CDP message of about 5.6 MB, and an open reader can retain up to 4 MiB of decoded data. | Custom `proto.Client` implementations and test doubles must not expect `Size` to equal the buffer length. If the connection limits messages to less than that, set a smaller `ChunkSize` on the returned reader before reading. |
| `StreamReader.Offset` | Holds the stream position of the next byte `Read` returns and advances by the bytes returned, rather than by the bytes received from CDP. The two differ only while data is buffered. Assigning `Offset`, or changing its value, before `Read` discards buffered data and reads from that position, including after EOF. | Seek by assigning `Offset`; data buffered from the previous position is not returned. To continue in another reader, start it at the current `Offset` value. |

### Page and endpoint data

These non-`Must` APIs return errors instead of panicking on page- or endpoint-controlled values. The corresponding `Must` helpers panic with the error through the configured fail function.

| Affected | Change | Migration |
| --- | --- | --- |
| `Element.CanvasToImage`, `Element.MustCanvasToImage` | A `toDataURL` result that is not a data URL or has invalid base64, such as a value from a page override, returns an error wrapping the new `ErrInvalidDataURL`. It no longer panics or yields partially decoded bytes. Data URLs are decoded with the percent-decoding and forgiving-base64 rules of the Fetch standard, so a body without `;base64` is percent-decoded rather than base64-decoded. A canvas without pixels still returns empty data. | Handle the error; use `errors.Is(err, rod.ErrInvalidDataURL)` to identify an invalid canvas result. |
| `Page.GetResource`, `Element.Resource`, `Element.BackgroundImage` | Invalid base64 resource content returns an error wrapping `base64.CorruptInputError`. | Handle the returned error. |
| `Page.ElementsByJS` and the element-list queries built on it (`Page.Elements`, `Page.ElementsX`, `Element.ElementsByJS`, `Element.Elements`, `Element.ElementsX`, `Element.Parents`) | A result array with an accessor property, including one produced by a page that replaces DOM query methods, returns `ExpectElementsError`. | Return arrays whose properties hold elements, and handle `ExpectElementsError`. |
| `Page.Search` | A search-result response without node IDs is retried like an unready search. | Bound searches with a context deadline or sleeper, as for other retries. |
| `Page.ExposeHelpers` | Returns an error instead of panicking through the fail function when its evaluation fails, such as when the page navigates or a page script makes the assignment to `window.rod` throw. The new `Page.MustExposeHelpers` panics with the error. | Handle the returned error, or call `MustExposeHelpers`. |
| `proto.Shape.Box`, `proto.DOMGetContentQuadsResult.Box` | Quads without a complete point are ignored; a shape without any point returns nil. `Element.MoveMouseOut` and `Element.Screenshot` then return `InvisibleShapeError`. | Check for a nil box. |

### Element lookup

Element queries reuse the execution context that produced their result and no longer create temporary window handles. Queries that return several elements release every remote object they do not return. The protocol requests these operations send change accordingly.

| Affected | Change | Migration |
| --- | --- | --- |
| `Page.ElementByJS`, `Element.ElementByJS`, `RaceContext.ElementByJS`, and their `Must` helpers | A non-null result that is not a DOM node is released before `ExpectElementError` is returned. The error still describes the value, but its `ObjectID` no longer refers to a live object. A failed release is joined with the error. | Use `Page.Evaluate` with `ByObject` to keep a result that is not an element. Use `errors.As` to read `ExpectElementError`. |
| `Page.ElementsByJS`, `Element.ElementsByJS`, the queries built on them such as `Elements`, `ElementsX` and `Element.Parents`, and their `Must` helpers | Remote objects that the query creates and does not return in an element are released. This includes the prototype handle that `Runtime.getProperties` reports. When `ExpectElementsError` is returned, the result and all its member handles are released. The error still describes the value, but its `ObjectID` no longer refers to a live object. A member defined by a getter returns `ExpectElementsError` describing the getter instead of panicking. The error is not wrapped when cleanup succeeds, and objects already reclaimed by navigation count as released. Other cleanup failures are joined with the error. | Use `Page.Evaluate` with `ByObject` to keep a result that is not an array of elements. Use `errors.As` or `errors.Is` to read `ExpectElementsError`. |
| `Page.ElementFromObject` | A nil object, or an object without `ObjectID`, returns `ExpectElementError`. Before, a nil object panicked and a missing ID sent an invalid protocol request. | Pass a remote object handle, such as the result of an evaluation with `ByObject`. |
| Protocol requests of element queries, frame views, and `Element.Interactable` | Queries on a page, frame, or element assign the result to the execution context that ran the call. Other objects are checked against the cached contexts with one `Runtime.callFunctionOn` each. The check starts with the page's own context, or, for a new view of a same-process frame, the context that frame last used. Only an uncached context creates a window handle, which is kept. `Interactable` resolves the node at its point directly and creates an `Element` only for `CoveredError`. Once helpers are cached, `Page.Element`, `Element.Parent`, frame queries, and element lookups in a new view of a known frame skip the context lookup. `Interactable` sends seven requests instead of eleven. `ElementsByJS` sends one more `Runtime.releaseObject`, concurrently with the release of its result. | Update custom clients, proxies, and test stubs that count requests, fail the nth `Runtime.callFunctionOn`, or match the former `() => window` lookup and its release. |

### JavaScript helpers

Rod installs its JavaScript helpers, which element queries and many `Page` and `Element` methods run, once per JavaScript context. Only one install runs in a context at a time. A call that needs a helper while another call installs helpers in the same context waits for that install and then uses the cached helper. If the waiting call's context ends first, it returns that context's error. Every helper of a context uses the same functions object. Before, calls that used a new document at the same time could install a helper into a functions object without its dependencies. Element queries in that document then failed with `TypeError: functions.selectable is not a function` until the page navigated.

| Affected | Change | Migration |
| --- | --- | --- |
| `Page.Evaluate` and the methods built on it, such as `Page.Eval`, element queries and same-process frame views | A `Runtime.evaluate` response for the page window without a remote object `objectId` returns an error and caches nothing. So do a `DOM.resolveNode` response for a frame document without one, a helper install response without one, and a `DOM.describeNode` response for a same-process frame's element without a `node`. A helper install that throws returns `*EvalError` and caches nothing. Before, a missing `result`, `object` or `node` panicked, a missing ID was cached and sent as an empty `objectId`, and a thrown value was cached as the helper. | Custom `proto.Client` implementations and test doubles must answer these requests as Chrome does: with a remote object that has an `objectId`, and for `DOM.describeNode`, with a `node`. |

### Retained command state

`Browser.LoadState` and `Page.LoadState` report only commands whose settings Rod reads back later. Parameters of every other command are released when the call returns. This includes large `Runtime.callFunctionOn` arguments, `Fetch.fulfillRequest` bodies, `Page.setDocumentContent` HTML, and `Input.insertText` text.

| Affected | Change | Migration |
| --- | --- | --- |
| `Browser.LoadState`, `Page.LoadState` | Only these commands are retained: domain enable commands until the domain's disable command, `Page.setLifecycleEventsEnabled`, `Page.setInterceptFileChooserDialog`, `Emulation.setDeviceMetricsOverride` until `Emulation.clearDeviceMetricsOverride`, and `Browser.setDownloadBehavior`. Every other command, including `Emulation.setGeolocationOverride` and `Target.createTarget`, reports `false`. | Record other settings in your application when you need to read them back. |
| `Browser.LoadState` with `proto.BrowserSetDownloadBehavior` | The setting belongs to the browser context named in the request. `LoadState` reads the context of the Browser it is called on and ignores `sessionID`. `Target.disposeBrowserContext` removes it, including through `Close` on an incognito Browser. | Call `LoadState` on the Browser whose context you configured. |
| `Browser.LoadState`, `Page.LoadState` after a session detaches | When `Target.detachedFromTarget` reports that a session detached, its retained commands and domain leases are removed. This includes sessions used through `Browser.PageFromSession` or a direct `Target.attachToTarget` call, which kept them until the connection ended. | Send the settings again on the new session. |
| Loaded parameters | `LoadState` decodes a new copy of the parameters that were sent. Changing the request after its call, or changing a loaded value, including its slices and pointers, no longer changes the retained command. Untyped parameters passed to `Browser.Call` or `Page.Call` load as the protocol type. Fields that the protocol encoding omits, such as empty slices, load as zero values. When `method` is not a pointer, `LoadState` reports whether the command is retained without loading parameters instead of panicking. | Pass a pointer to the protocol request type. Send the command again to change a retained setting. |
| `Browser.EnableDomain`, `Browser.DisableDomain`, `Page.EnableDomain`, `Page.DisableDomain` | Accept only domain enable commands and `Page.setLifecycleEventsEnabled`. Other requests return `ErrUnsupportedDomain` without sending a command. | Send other commands directly and restore their settings explicitly. |

### Page attachment

| Affected | Change | Migration |
| --- | --- | --- |
| `Browser.PageFromTarget`, `Browser.Page` | Concurrent calls for different targets attach, emulate the default device, and enable the Page domain concurrently rather than one target at a time. Concurrent calls for the same target still share one attachment. If that attempt fails or its caller is canceled, each waiting call attaches again using its own context. | Custom `CDPClient` implementations must handle concurrent `Call` invocations. Each caller receives its own attachment error or cancellation. |

### DOM stability waits

`Page.WaitDOMStable` tracks DOM changes with a mutation observer inside the page instead of comparing DOM snapshots. Rod times the stability period and checks the observer with one small protocol call per period, so protocol traffic does not grow with the DOM size and the period does not depend on page timers. The wait returns once `d` passes without changes beyond the limit, instead of comparing snapshots taken `d` apart. `Page.WaitStable`, `Page.MustWaitDOMStable`, `Page.MustWaitStable`, and the wait after each scroll step of `Page.ScrollScreenshot` behave the same way.

| Affected | Change | Migration |
| --- | --- | --- |
| `diff` argument of `Page.WaitDOMStable` | A fraction from 0 to 1 of the nodes present when observation of the document began. Inserted and removed nodes count with their descendants, nodes whose attributes or character data change count once, and each node counts at most once per period. A change beyond the limit restarts the period. Values outside 0–1 and NaN return an error. Previously `diff` was compared with the changed share of a DOM snapshot's string table, and any value was accepted. | Pass `0` to require a period without changes. Express tolerance as a fraction of nodes; replace values above 1 with 1. |
| What counts as a change | A change that a script undoes before the observer receives it, such as a node inserted and removed again in one task, or an attribute or text restored to its previous value, is not a change. State that the DOM tree does not reflect, such as form control values, is not observed. | Wait for other conditions with `Page.Wait` or element queries. |
| Observed content | The wait observes the page's document and its open shadow roots. Shadow roots of inserted elements are observed at once; a shadow root attached to an element already in the document is found when the period would end, which restarts the period. Iframe documents and closed shadow roots are not observed. | Call `WaitDOMStable` on the frame returned by `Element.Frame` to wait for an iframe document. |
| Pages with scripts disabled | The page cannot deliver the observer's records, so each check compares a digest of the DOM, which takes time proportional to the DOM size. Any change restarts the period regardless of `diff`. | None. |
| Navigation, cancellation, and errors | When navigation replaces the document, a new period begins in its successor at the wait's next check. When the context ends, the wait returns the context error at once and removes its observer from the page separately, without reporting cleanup failures; after a completed wait, cleanup failures are joined to the returned error. `DOMSnapshot` errors no longer occur; script errors return `*rod.EvalError`, and malformed protocol responses return errors. | Check returned errors with `errors.Is` or `errors.As`. Bound waits with `Page.Timeout` or a context deadline. |

The wait runs in the page's main world and uses the page's `MutationObserver`, `EventTarget`, `setTimeout`, and `pagehide` event. A page that replaces these globals affects the wait.

### Scroll screenshots

| Affected | Change | Migration |
| --- | --- | --- |
| `ScrollScreenshotOptions.FixedTop`, `FixedBottom`, `WaitPerScroll` | Negative or NaN values, and fixed areas that together cover the viewport height, return `rod.ErrScrollScreenshotOptions` before capturing. Such options previously failed in the browser or captured the same region until the context ended. | Keep `FixedTop + FixedBottom` smaller than the viewport height and `WaitPerScroll` nonnegative. |
| `ScrollScreenshotOptions.MaxHeight`, `Page.ScrollScreenshot` | Page content taller than `MaxHeight` CSS pixels, 32768 by default, returns `rod.ErrScrollScreenshotTooTall` before capturing. Layout metrics without a positive viewport size also return an error. Previously any content height was captured and stitched. | Set `MaxHeight` to capture taller pages, allowing four bytes of memory per stitched device pixel. |
| `Page.ScrollScreenshot` options argument | The default `WaitPerScroll` is no longer written into the caller's options. | Do not read defaults back from the options after the call. |

### Image stitching

| Affected | Change | Migration |
| --- | --- | --- |
| `utils.SplicePngVertical` | Reads every image's size from its header before decoding pixels, then decodes one image at a time. Returns an error for a `Box` that is inverted or not within its image, for an image or a result larger than `utils.MaxSplicePixels` (2^28 pixels), and for a JPEG result wider or taller than 65535 pixels. Previously boxes beyond the image produced decoder-specific fill colors, and oversized results panicked, attempted the allocation, or failed while encoding. Output for valid boxes is unchanged. | Clip boxes to the image bounds with `image.Rectangle.Intersect`. Stitch larger results in parts. |

### Trace output and overlays

`Page.Overlay` and `Element.Overlay` display their message as plain text. HTML
markup in a message is shown literally and is never parsed or executed. Trace
overlays use the same rendering; query traces show monospace text instead of a
`<code>` element.

Trace logs and overlays no longer show values that automation types or enters:

| Affected | Change | Migration |
| --- | --- | --- |
| `Page.InsertText`, `Element.Input`, and their Must helpers | `insert text <text>` becomes `insert text (N characters redacted)`, where N counts Unicode characters. | Do not rely on trace output to recover typed text; log it yourself where that is safe. |
| `Keyboard.Press`, `Keyboard.Release`, `Keyboard.Type`, `KeyActions.Do`, `Element.Type`, and their Must helpers | Keys that type a visible character or a space trace as `press character key (redacted)` or `release character key (redacted)`. Enter, Tab, modifiers, arrows, and other named keys still trace as `press key: <Code>`. | Match the new wording in custom `Browser.Logger` implementations. |
| `Element.InputTime`, `Element.InputColor`, and their Must helpers | Messages become `input time (value redacted)` and `input color (value redacted)`. | Match the new wording. |
| `Page.Overlay`, `Element.Overlay` | The message is plain text. | Pass plain text. To display custom markup, create the elements yourself with `Page.Eval` or `Element.Eval`. |

### Monitor server

`Browser.ServeMonitor`, `Browser.Monitor`, and the `-rod=monitor` option serve
the monitor below a random access-token path and validate the Host header.

| Affected | Change | Migration |
| --- | --- | --- |
| `Browser.ServeMonitor` return value | Returns `http://<listener>/<token>/` instead of `http://<listener>`. Each call generates a new token with at least 128 bits of randomness. | Use the returned URL as is. Append `page/<target ID>`, `api/pages`, `api/page/<target ID>`, or `screenshot/<target ID>` to it, without a leading slash, instead of appending root paths to the listener address. |
| `Browser.Monitor`, `-rod=monitor` | The listen address alone no longer opens the monitor. `Browser.Connect` logs the full URL through `Browser.Logger` as `[monitor] <URL>`, even when tracing is disabled, and then tries to open it in the system browser. `DefaultLogger` writes the line to standard output. | On a headless or remote machine, copy the logged URL. Custom loggers receive `rod.TraceTypeMonitor` followed by the URL. To get the URL in code, call `Browser.ServeMonitor` instead. |
| Monitor routes | Requests outside the token path, including `/`, `/api/pages`, `/page/…`, and `/screenshot/…` at the server root, return 404 Not Found. `/<token>` redirects to `/<token>/`. | Open or bookmark the complete returned or logged URL. |
| Host header | The Host must be `localhost`, a loopback IP, the listener address, or the local IP address and port the connection reached. Other requests, including those using DNS names of the machine, return 403 Forbidden. | Open the monitor by IP address or through `localhost`. Through an SSH tunnel, open the token path on the tunnel's local port, for example `http://localhost:8080/<token>/`. Configure an authenticated reverse proxy to send a loopback or listener Host upstream, such as nginx's default for `proxy_pass http://127.0.0.1:<port>`. |
| `assets.Monitor`, `assets.MonitorPage` | The pages request `api/pages`, `page/<id>`, `../api/page/<id>`, and `../screenshot/<id>` relative to their own URL instead of using root paths. | When serving these assets yourself, serve the page list at a URL ending in `/`, the page view at `page/<id>` below it, and the API and screenshot routes below the same directory. |

The token grants access to anyone who has the URL; it does not authenticate
users. Keep the monitor on loopback or behind a trusted authenticated proxy.
`launcher.Open`, which `Browser.Monitor` uses, passes the URL to the browser as a
command-line argument that other users of the same machine may be able to read.
On shared machines, call `Browser.ServeMonitor` and paste the URL into a browser
instead.

### CDP WebSocket limits

The built-in `cdp.WebSocket` bounds the data it accepts from a browser endpoint and the time it waits to connect.

| Affected | Change | Migration |
| --- | --- | --- |
| `cdp.WebSocket.Read`, `cdp.Client.Call`, `cdp.Client.Event` | An incoming message larger than `cdp.WebSocket.MaxMessageSize` fails with `cdp.ErrWebSocketMessageTooLarge`, and the connection closes with WebSocket status 1009. Pending and later calls return that error, and the event channel closes. The limit counts all fragments of a message. Zero or a negative value selects `cdp.DefaultMaxMessageSize` (256 MiB). | Keep screenshots, PDFs, and evaluation results below the limit, for example by clipping, or connect the transport with a larger `MaxMessageSize` as shown below. `math.MaxInt64` accepts any size. |
| `cdp.WebSocket.Connect`, `cdp.StartWithURL`, `cdp.MustStartWithURL`, `cdp.MustConnectWS`, `Browser.Connect`, `launcher.Launcher.Client`, `launcher.Launcher.MustClient` | Dialing, TLS, and the HTTP upgrade must finish within `cdp.WebSocket.HandshakeTimeout`, 30 seconds by default (`cdp.DefaultHandshakeTimeout`), in addition to the context deadline. The error satisfies `errors.Is(err, context.DeadlineExceeded)`. The timeout does not apply after `Connect` succeeds. | For endpoints that complete the upgrade slowly, set `HandshakeTimeout` on your own `cdp.WebSocket`. A negative value leaves only the context in effect. |
| `cdp.Dialer`, `cdp.WebSocket.Dialer` | `Connect` passes the dialer a context derived from its own and cancels it when `Connect` returns. A connection that closes when its dial context ends is lost right after `Connect` succeeds. | Keep the dialed connection independent of the dial context, as `net.Dialer` does. A negative `HandshakeTimeout` passes the caller's context to the dialer unchanged. |
| `cdp.WebSocket.Connect` | An upgrade response larger than 1 MiB, headers included, fails with `cdp.ErrWebSocketProtocol`. | Keep headers added by proxies in front of the endpoint below 1 MiB. |
| `launcher.ResolveURL`, `launcher.MustResolveURL` | A `/json/version` response body larger than 1 MiB is rejected with an error. | Serve a standard discovery response, or resolve the WebSocket URL yourself and pass it to `Browser.ControlURL`. |
| `launcher.NewManaged`, `launcher.MustNewManaged` | A launch-settings response from the manager larger than 1 MiB is rejected with an error. | Keep the settings returned by `Manager.Defaults` small. |

To change these limits for a `Browser`, connect the transport yourself and pass the client:

```go
ws := &cdp.WebSocket{MaxMessageSize: 1 << 30, HandshakeTimeout: 2 * time.Minute}
if err := ws.Connect(ctx, controlURL, nil); err != nil {
    return err
}
browser := rod.New().Client(cdp.New().Start(ws))
if err := browser.Connect(); err != nil {
    return err
}
```

### CDP write cancellation

An interrupted write no longer always closes the built-in WebSocket connection. This replaces the v0.120.0 behavior, in which canceling any active write closed it.

| Affected | Change | Migration |
| --- | --- | --- |
| `cdp.WebSocket.SendContext`, `cdp.Client.Call` | If cancellation or a deadline interrupts a write before the connection accepts any byte of the frame, the frame is abandoned. The connection stays open and the call returns the context error. A partially written frame still closes the connection. TLS connections close after any failed write, and other write errors close the connection. | Do not treat one request's context error as connection loss, and do not reconnect after every interrupted write. Detect disconnection through errors from later calls or the closed event channel, and reconnect only then. |

### Remote manager launch options

| Affected | Change | Migration |
| --- | --- | --- |
| `launcher.Manager` launch option names | Names must be canonical Chromium switch names: lowercase ASCII letters, digits, and hyphens, starting with a letter or digit. Other spellings, including mixed case, underscores, and surrounding spaces, are rejected with HTTP 400. Chromium lowercases switch names on Windows, so such spellings could select a restricted or manager-owned switch. | Use the lowercase switch names that Chromium documents. |
| `launcher.Manager` positional arguments (`flags.Arguments`, `Launcher.StartURL`) | An argument is rejected when it starts with `-` or `/` after leading whitespace or other non-printing characters. Chromium trims whitespace before it detects a switch, so such an argument was parsed as a switch. | Pass browser switches as named launch options. |
| `launcher.Manager` restricted switches | Also rejects switches that load host code or set process roles (`js-flags`, `type`, `load-apps`, `load-and-launch-app`, `install-isolated-web-app-from-file`, `pack-extension`, `nacl-gdb`), make the browser write host files (`enable-logging`, `log-file`, `log-net-log`, `ssl-key-log-file`, `webrtc-event-logging`, `dump-browser-histograms`, `export-uma-logs-to-file`, `export-ukm-logs-to-file`, `profiling-file`, `ozone-dump-file`, `list-apps`, `focus-result-file`, `print-to-pdf`, `screenshot`, `trace-*`, `enable-tracing*`), expose DevTools outside the manager (`remote-debugging-*` other than the port, `remote-allow-origins`, `custom-devtools-frontend`, `allow-unsafe-devtools-remote-file-loading`), delegate host credentials (`auth-server-allowlist`, `auth-negotiate-delegate-allowlist`, and their former `whitelist` names), or make Chromium on Windows drop the switches that follow (`single-argument`). Names containing the words `dir`, `directory`, `launcher`, `library`, `path`, or `prefix` are rejected, except the manager-owned `user-data-dir` and the validated `profile-directory`. Unknown `rod-*` options are rejected. | Configure trusted server-side switches in `Manager.BeforeLaunch`. Do not return restricted switches from `Manager.Defaults`: managed clients send those settings back, and the request is rejected. See [remote manager security](../lib/launcher/README.md#remote-manager-security). |
| `launcher.Launcher.Launch`, `launcher.Launcher.MustLaunch` | On a launcher from `launcher.NewManaged` or `MustNewManaged`, `Launch` returns the existing `launcher.ErrManagedLaunch` and `MustLaunch` panics with it, as `LaunchNew` already did. Before, `Launch` started a local browser with the manager's settings, including the executable, environment, and working directory, so a manager, or whatever answered at its URL, chose the program that ran on the client. | Connect through the manager with `Launcher.Client` or `MustClient`. To start a local browser, use `launcher.New`. |

### Generated browser profiles

| Affected | Change | Migration |
| --- | --- | --- |
| `launcher.DefaultUserDataDirPrefix` | Removed. | Set `TMPDIR` on Unix, or `TMP` on Windows, to choose the parent directory of generated profiles. Use `Launcher.UserDataDir` for a caller-owned directory. |
| `launcher.New` default `UserDataDir` | A generated profile is a `rod-profile-<random>` directory directly inside `os.TempDir()`, not below a shared `rod/user-data` directory. Launch no longer creates missing parent directories and fails if anything, including a symlink, already exists at the profile path. | Ensure the temporary directory exists. Read the generated path with `Launcher.Get(flags.UserDataDir)` instead of assuming a location. |

### Command-line defaults

| Affected | Change | Migration |
| --- | --- | --- |
| `defaults.Load`, `defaults.ResetWith`, `-rod` | `-rod` is read only from the leading flags, using the flag package's rules. Reading stops at `--`, at the first non-flag argument, and at an undefined flag without `=value`, other than an undefined `test.*` flag; the value of another flag is never read as `-rod`. After `flag.CommandLine` is parsed, only the parsed value of a defined `-rod` flag is used. `go test -- -rod=...` no longer applies the options. | Put `-rod` among the leading flags and pass untrusted arguments after `--`. A test binary accepts `-rod` only when `-rod` is defined before the testing package parses flags: call `defaults.Load()` in `TestMain` before `m.Run`, or create a Rod object during package initialization. Then pass it to that package, as in `go test ./tests -rod=show`. `go test ./... -rod=show` fails with "flag provided but not defined: -rod" in packages that do not define it. Place `-rod` before flags handled by other parsers, use their `-name=value` form, or define standard flags before calling `Load`. |
| `-rod` defined by `defaults.Load` | `flag.Parse` applies each `-rod` value it parses, in order, and reports an invalid value as a flag error. Repeated `-rod` flags no longer keep only the last one: later values override the same options, and other options from earlier flags remain. | Assign exported defaults after `flag.Parse`. Put all options in a single `-rod` value when earlier flags should not contribute. |

### Test workflows

Tests of the root package that use only its exported API, including browser-free tests with fake protocol clients, moved from the repository root to `tests/`. The root keeps the tests that need unexported identifiers and the Go documentation examples. `go test .` no longer runs the moved tests. `bash scripts/check.sh pure` runs the browser-free tests in both locations, and `bash scripts/check.sh browser` runs the complete suite; see [test commands](../tests/README.md). Public APIs are unchanged by this relocation.

## v0.124.0 — compared with v0.123.0

### Resource cleanup

`launcher.Launcher.Context` cancels its previous internal context when replacing
it. Operations and managed clients still using that context are canceled.
Configure the context before launching or creating a managed client.

Element lookup, `Page.HTML`, and interactability helpers release their internally
owned remote objects. Cleanup failures can return errors or panic in `Must`
helpers. Cleanup uses a separate bounded context after operation cancellation;
objects already reclaimed by navigation count as released. Check returned errors
and use `errors.Is` or `errors.As` to inspect causes when operation and cleanup
errors are joined.

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
