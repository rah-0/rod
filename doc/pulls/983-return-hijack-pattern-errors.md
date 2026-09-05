# Return hijack pattern compilation errors without partial registration

Priority: P2. Source: [upstream PR #983](https://github.com/go-rod/rod/pull/983), associated with [issue #982](https://github.com/go-rod/rod/issues/982).

[`HijackRouter.Add`](../../hijack.go) returns an error but invokes `regexp.MustCompile` on the output of [`proto.PatternToReg`](../../lib/proto/a_utils.go). User-supplied patterns can therefore panic before `Add` returns. Calling `Add("[", ...)` reproduces the panic after the enabled-pattern list has already grown.

Compile the converted pattern using the error-returning API before changing router state or sending `Fetch.enable`. Return the compilation error through the existing `Add` result. Preserve the behavior of `MustAdd`, which should continue to route returned errors through the configured panic handler.

The upstream patch removes the direct panic but leaves pattern registration before compilation. Applying it unchanged would leave an invalid entry in `r.enable.Patterns` after a failed call, potentially affecting a subsequent successful registration. Fix that ordering as part of this task. A broad replacement of constant `regexp.MustCompile` expressions elsewhere is unnecessary; the relevant boundary here is the caller-supplied pattern.

Acceptance criteria:

- A pattern whose converted regular expression is invalid returns an error from `Add` without a panic.
- Failed compilation leaves enabled patterns and handlers unchanged and sends no CDP registration command.
- A subsequent valid registration succeeds and matches as before.
- Existing wildcard and escaped-wildcard behavior remains covered by focused pattern tests.
- A mocked protocol client verifies registration behavior without requiring a browser.
