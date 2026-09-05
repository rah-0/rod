# Evaluate JavaScript under an explicit JSON contract

Priority: P2. Status: proposed.

## Value and current support

Browser tests and automation often need a Go struct or scalar from JavaScript.
They also need to distinguish valid JSON data from an unsupported result instead
of accepting an omitted field or a substituted null.

[Page.Eval](../../page_eval.go) already awaits promises and returns by value.
`result.Value.Unmarshal(&destination)` already decodes into Go types using
[jsonvalue](../../lib/jsonvalue/value.go). `Page.Evaluate` additionally supports
remote objects and evaluation options. None of those need replacement.

The missing behavior is an opt-in helper that combines browser-side JSON
serialization, explicit rejection of unsupported visited values, and decoding
into a validated Go destination. Typed decoding by itself is already available.

## Proposed behavior

Start with a page helper using the same trusted JavaScript function and argument
conventions as `Page.Eval`, plus a destination. Pass data as arguments rather
than interpolating it into JavaScript. Await a returned promise, serialize the
resolved result in the browser, and decode the returned JSON string with standard
`encoding/json` semantics.

Define these cases explicitly:

| Input or result | Behavior |
| --- | --- |
| Non-nil Go pointer | Validate it before evaluating; decode using standard JSON field tags, custom unmarshaling, and type/range checks. |
| Nil interface destination | Execute and await the function for its side effects; discard the value without serializing it or retaining a remote object. |
| Typed nil or non-pointer destination | Return a destination error before running JavaScript. |
| JSON null | Decode as JSON null, distinct from an undefined result. |
| Undefined, function, symbol, bigint, or non-finite number visited during serialization | Return a serialization error, including when encountered in an array or enumerable property. |
| Cycle or serialization exception | Return a serialization error. |
| Thrown exception or rejected promise | Return an evaluation error. |
| Incompatible Go destination | Return a decode error that preserves its underlying cause. |

Use `JSON.stringify` with validation during traversal. Its normal `toJSON`
behavior still applies: dates serialize as strings, and custom `toJSON` methods
can transform values before validation sees them. Symbol-keyed and
non-enumerable properties are not visited. Maps, Sets, Errors, and DOM objects
are not automatically converted into meaningful application data; callers
should project them to ordinary JSON values.

The contract rejects unsupported values that serialization visits. It does not
claim lossless validation of an arbitrary JavaScript object graph. Serialization
can execute application-defined getters or `toJSON` methods, just as an explicit
`JSON.stringify` call does. JavaScript number precision is unchanged.

Preserve useful error context for evaluation, serialization, and Go decoding.
Honor the page context and Rod's existing evaluation behavior across navigation;
do not add another retry layer or promise exactly-once application side effects.
Keep the current `Eval`, remote-object, and `jsonvalue` contracts intact.

## Acceptance

- Decode scalars, structs with JSON tags, arrays, maps, null, and resolved
  promises; cover custom Go unmarshaling and a JavaScript Date/custom `toJSON`.
- Reject unsupported root values and visited nested values, non-finite numbers,
  sparse array entries, cycles, and throwing serialization hooks.
- Demonstrate the documented treatment of symbol-keyed properties and explicit
  projections of Map/Set/DOM data.
- Invalid Go destinations fail before any JavaScript side effect. A nil
  interface destination permits an otherwise unserializable result to be
  discarded after its effects and promise have completed.
- Distinguish undefined from null and preserve standard JSON decode errors for
  incompatible types and numeric overflow. Do not coerce results to fit a target.
- Cancellation, promise rejection, and navigation failures terminate through the
  existing context/error model without retained temporary remote objects.
- If implemented as a shared JavaScript helper, update
  [the helper source](../../lib/js/helper.js) and generated output together.
