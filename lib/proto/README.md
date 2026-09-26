# Overview

A lib to encode/decode the data of the cdp protocol.

This lib is standalone and stateless, you can use it independently. Such as use it to encode/decode JSON with other libs that can drive browsers.

Here's an [usage example](https://github.com/rah-0/rod/blob/9e847f3bab313a1d233c0c868fe5125e2e70de70/examples_test.go#L370-L393).

See [protocol generation](generate/README.md) for generation from the installed
browser, freshness checks, provenance, and explicit offline input.

## Required fields

The generated `Call` methods decode command results with `Unmarshal`, which
checks that the result has every field the protocol schema requires. It
returns a `*MissingFieldError`, matching `ErrMissingField` with `errors.Is`,
when:

- a required member of any type, such as a string, number, boolean, array,
  map, or protocol object, is missing or `null`;
- an array contains `null`.

The rules apply at every depth, including inside optional objects that are
present. The error names the decoded type and the JSON path of the missing
field, for example
`proto: DOMGetDocumentResult: root.children[2].nodeName is missing or null`.
A path that ends in an index refers to a `null` array entry. A result that
fails the check is still returned with the fields that were decoded.

`null` is accepted where it is a value: for `jsonvalue.Value` fields and
entries, and for `float64` numbers, which Chrome sends as `null` when JSON
cannot represent them, such as infinity. Such numbers keep their zero value.

The checks follow the schema of the browser that the bindings were generated
from, and other browser versions can differ from it. Members that the schema
marks as experimental or deprecated are therefore not required: an older
browser can lack an experimental member, and a newer one can drop a deprecated
member. Members that older browsers omit are not required either. The
generator records them in
[`generate/schema-compatibility.json`](generate/schema-compatibility.json),
and their documentation starts with `(optional in older browsers)`. For
example, Chromium 128 sends `Debugger.scriptParsed` without `buildId` and
answers `Network.getRequestPostData` without `base64Encoded`. Members that are
not required keep their zero value, or are nil, when they are missing. A
browser or endpoint that omits any other required member fails the check, even
when the caller does not read that member.

Decoding uses generated code without reflection and reads the input once.
Member names must match the protocol's names exactly, including case, binary
fields accept only base64 strings, and decoding stops at the first error;
otherwise decoding follows `encoding/json`. `Decoding.Unmarshal` documents the
details.

Decode events and other protocol values with `Unmarshal` instead of
`encoding/json` to apply the same checks. Command parameters, and types used
only by them, are decoded with `encoding/json` and are not checked. A `Client`
must answer commands with complete results: a test double that answers
`Runtime.evaluate` with `{}`, or with `{"result":{}}` without the remote
object's `type`, receives `ErrMissingField`. Go's `encoding/json` encodes nil
slices and maps as `null`, so a test double that encodes the generated types
must set required slices and maps, such as `NetworkRequest.Headers`, to empty
values.

### Lenient decoding

`DecodeLenient.Unmarshal` accepts data that lacks required fields, has
`null` for them, or has `null` array entries: such fields and entries keep
their zero value, which is nil for objects and arrays. A `null` entry of an
array of numbers, strings or booleans is therefore 0, an empty string or
false. A `Client` that implements `Decodable` selects the decoding of its
command results; `rod.Browser.Decoding` sets it for a browser, its pages and
elements, and the events it receives. Use it only for endpoints that do not
send complete protocol data. The decoded values then do not necessarily hold
what the protocol requires.

Rod's methods check the objects, arrays and object entries that they use.
Instead of panicking or returning a nil result, a method whose result or event
lacks one returns an error matching `ErrMissingField`: a `*MissingFieldError`
that names the type and path as strict decoding does. For example, `Page.Eval`
reports a `RuntimeCallFunctionOnResult` without `result`, `Browser.Pages` a
`null` entry of `targetInfos`, and `Page.WaitOpen` a `Target.targetCreated`
event without `targetInfo`. Rod's methods also treat an empty identifier, or
execution context ID 0, as missing where they would use it to address a
browser context, target, session, search, script or execution context. For
example, a page attached without the session ID of `Target.attachToTarget`
would send its commands to the browser, and an incognito browser without the
browser context ID of `Target.createBrowserContext` would use the default
context. Some methods continue after such an event:

- `HijackRouter` fails a paused request that lacks its request details,
  without running handlers, and reports the error through its `OnError`.
- `Browser.HandleAuth` gives the browser's default response to a challenge
  without its details or its source, and, unless `AnyOrigin` is set, to one
  without its origin.
- `Page.Expose` ignores a binding call without its execution context.
- `Browser.WaitDownload` ignores targets and target events without their
  target information or target ID, and downloads without their frame ID.
- Page diagnostics skip the event and report that they are incomplete.

Lenient decoding still has these risks:

- Other missing fields keep their zero value, including IDs that events are
  matched by, and Rod's methods act on it as if the endpoint had sent it. For
  example, an element without `outerHTML` has empty HTML, and a search result
  without `resultCount` makes `Page.Search` search again. So does a `null`
  node ID in the search results, which reads as the node ID 0 that the browser
  reports after the document changed.
- Values that Rod returns or passes on unchanged can hold nil objects and
  entries, such as the node that `Element.Describe` returns, the result of
  `Page.GetNavigationHistory`, and the events that event handlers,
  `WaitEvent` and `Event` receive.
- Code that calls the generated `Call` methods or `Unmarshal` itself, and
  helpers such as `CookiesToParams` that receive such values, get the lenient
  values. Check the fields that such code reads.

The [tab metadata example](../../examples/tab-metadata/README.md) queries tab
targets through `TargetGetTargets.Filter` and preserves optional and unknown
`TargetTargetInfo.EmbedderData` fields without activating tabs.
