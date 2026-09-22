# input

See the [runnable example](../../examples/typing/main.go).

A lib to help encode inputs.

[`Keys(text)`](text.go) validates a complete string and converts it to keys for
`Element.Type` or `Keyboard.Type`:

```go
keys, err := input.Keys("Hello!\n")
if err != nil {
    return err
}
element, err := page.Element("#message")
if err != nil {
    return err
}
return element.Type(keys...)
```

Validate before focusing or typing. Errors return no partial sequence and include
the unsupported character or invalid UTF-8 byte and its byte offset. Use
`errors.Is` with `input.ErrUnsupportedCharacter` or `input.ErrInvalidUTF8` to
distinguish them. A later protocol error can still leave partially entered text.

The existing keyboard map supports letters, digits, punctuation, shifted
characters, and space. Line feed (`\n`) maps to Enter; tab (`\t`) and carriage
return (`\r`) are rejected. Empty text returns an empty sequence. Character
descriptors are checked so unsupported Unicode cannot become a named key; for
example, U+0108 (`Ĉ`) shares Backspace's numeric ID and is rejected.

Shifted characters use Rod's existing event metadata without separate Shift
presses. `Element.Type` focuses and types at the current selection, preserving
text outside it. Enter can trigger an application action instead of inserting a
newline. This mapping does not detect operating-system layouts or perform IME
composition, drop characters, or fall back to text insertion.

For arbitrary Unicode insertion, use `Element.Input` or `Page.InsertText`:

```go
err := element.Input("こんにちは 🦊")
```

Keyboard listeners observe different events for insertion and typing.
[Mapping tests](text_test.go) and [browser tests](../../tests/input_text_test.go) cover
validation, keydown/keyup events, Enter actions, and preservation of existing text.

Use the named `Numpad0` through `Numpad9` and `NumpadDecimal` keys for numeric
keypad input. Their descriptors and dispatched virtual-key codes represent
digits and decimal, with the keypad event flag set. On macOS, editing commands
use the modifier combination and physical key code, and run only on key-down.

`Key.Printable` recognizes a single Unicode code point in a registered key's
descriptor. Keys returned by `AddKey` can therefore type registered multibyte
characters. Keyboard-layout registration and shifted mappings remain the
caller's responsibility.
