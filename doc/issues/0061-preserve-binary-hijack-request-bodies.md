# Preserve available binary data in intercepted request bodies

Priority: P2 · Protocol compatibility · Confirmed with synthetic CDP input on 2026-09-05.

Sources: [upstream issue #61](https://github.com/go-rod/rod/issues/61), related missing-body report [#1018](https://github.com/go-rod/rod/issues/1018).

The original claim that CDP provides only string request bodies is outdated for the protocol models in this repository. [NetworkRequest](../../lib/proto/network.go) has `PostDataEntries`, whose entries contain decoded `[]byte`; the string `PostData` is deprecated. [Current CDP documentation](https://chromedevtools.github.io/devtools-protocol/tot/Network/#type-Request) describes those entries as request body data.

[HijackRouter.new](../../hijack.go) constructs the outgoing HTTP body exclusively from `PostData`, and [HijackRequest.Body](../../hijack.go) reads that same string. Available binary entry data is discarded. A synthetic paused request with `HasPostData=true`, empty `PostData` and bytes `ff 00 61` in `PostDataEntries` produced an empty forwarded body and an empty `Body()` result. This confirms the loss when entries are provided; it does not establish which browser versions emit entries for each request kind or reproduce the specific website in #1018.

A separate live Chrome 152.0.7977.64 fixture successfully forwarded a typed-array POST containing `ff 00 61` through `LoadResponse`. Ordinary binary POSTs are therefore not generally broken; this task covers the documented entry-only representation that the current parser ignores.

Use the available entry bytes without a lossy text round trip, retain the legacy string fallback, and define consistent access to the reconstructed body through the existing API. Correct the unsupported-binary comment. Keep this task limited to bytes CDP actually provides: omitted file content or absent entries must not be invented or described as successfully recovered.

Acceptance criteria:

- Non-UTF-8 data, NUL bytes, multiple entries, an empty body and the legacy string representation preserve their exact outgoing bytes.
- Distinguish an explicitly empty request body from unavailable body data where the protocol supplies that distinction.
- A local browser fixture exercises a typed-array request through `LoadResponse` and checks server-received bytes.
- Existing textual/JSON hijacking and caller-supplied `SetBody` continue to work.
