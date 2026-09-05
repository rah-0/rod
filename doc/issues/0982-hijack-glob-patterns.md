# Translate CDP hijack globs without regexp panics or unintended matches

- Priority: P2
- Sources: [#982](https://github.com/go-rod/rod/issues/982), proposed error-handling patch [PR #983](https://github.com/go-rod/rod/pull/983).
- Reviewed: 2026-09-05.
- Validation: reproduced with `proto.PatternToReg`: `**.example.com/**` becomes `\A.**.example.com/.**\z`, which fails regexp compilation with an invalid nested repetition error.

[HijackRouter.Add](../../hijack.go) appends a CDP pattern and then calls `regexp.MustCompile`. [PatternToReg](../../lib/proto/a_utils.go) uses overlapping replacement patterns that mishandle adjacent wildcards and leave regexp metacharacters unescaped. A documented CDP URL glob can therefore panic or match a different set of URLs in Rod than in the browser. `Add` already returns an error, but this failure bypasses it and can leave partially changed router configuration.

Translate the documented CDP wildcard and escaping rules directly, treating ordinary URL characters literally. Compile and validate the resulting matcher before changing router state. If a pattern cannot be accepted, return the error through `Add`; retain the existing panic behavior of the explicit `MustAdd` wrapper. PR #983 addresses error propagation but needs review against the actual glob semantics.

Acceptance criteria:

- Adjacent `*`/`?` wildcards and backslash-escaped wildcards follow `FetchRequestPattern.URLPattern` semantics without a Go panic.
- Literal dots, plus signs, brackets, and other regexp metacharacters in URLs match literally.
- Rejected additions leave the existing router patterns and handlers usable.
- Pure table tests cover translation and matching; a focused interception fixture checks representative patterns against CDP.
