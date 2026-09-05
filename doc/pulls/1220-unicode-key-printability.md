# Recognize registered multibyte character keys as printable

Priority: P2.

Source: upstream [#1220](https://github.com/go-rod/rod/pull/1220).

## Current evidence

[Key.Printable](../../lib/input/keyboard.go) checks whether `KeyInfo.Key` has one byte. A Unicode character can occupy several UTF-8 bytes, so registered non-ASCII character keys are treated as named control keys. `Key.Encode` consequently emits `rawKeyDown` and omits the text that Chrome needs to insert the character.

For example, after registering `б` with `input.AddKey`, `Printable()` returns `false`, and encoding a key-down event produces `Type: rawKeyDown`, `Text: ""`.

## Change

Recognize a single Unicode code point instead of a single byte, using the standard library without adding dependencies. Preserve the existing distinction between character keys and named keys such as `Shift` and `ArrowLeft`. Adapt upstream tests to the fork's standard test utilities and pointer conventions.

Keep the claim precise: this corrects event encoding for registered keys. `AddKey` also contains byte-based registration and shifted-key logic; this patch alone does not provide complete Unicode keyboard-layout registration or arbitrary text input support.

## Acceptance

- Registered Cyrillic, accented Latin, and supplementary-plane character keys are printable and emit `keyDown` with matching `Text` and `UnmodifiedText`.
- ASCII character events remain unchanged.
- Named modifiers/navigation keys remain non-printable and keep their existing event type and empty text.
- Existing Enter and shifted ASCII behavior remain covered.
- Focused `lib/input` tests pass without introducing a new dependency or expanding the public API.
