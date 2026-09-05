# Type supported text through keyboard events

Priority: P2. Status: proposed.

## Value and current support

Applications that observe keyboard events need more than pasted text. Turning a
Go string into Rod key constants currently requires custom validation, and a
naive rune conversion can panic or send the wrong key.

[Element.Type](../../element.go) and [Keyboard.Type](../../input.go) already
dispatch key sequences. `Element.Input` and `Page.InsertText` already support
arbitrary Unicode text insertion. The missing operation is safe conversion of
supported text to the existing key representation.

[input.Key](../../lib/input/keyboard.go) uses a rune-sized integer for both
characters and synthesized named-key IDs; `Key.Info` panics when an ID is
unknown. Checking only whether `input.Key(character).Info()` succeeds is not
valid text validation. For example, [the Backspace definition](../../lib/input/keymap.go)
produces ID `8 + 256`, which equals U+0108 (`Ĉ`). Casting that character can
therefore produce Backspace instead of rejecting unsupported text.

## Proposed behavior

Add one error-returning string-to-key conversion helper in `lib/input` that
composes with the existing `Type` methods. A separate `TypeText` method is only
worth adding if that composition proves insufficient; do not add both APIs
automatically.

Resolve characters through their actual character descriptors in the existing
key maps. Support shifted characters and explicitly translate line feed to
Enter. Specify how tab and carriage return are treated; the initial contract
should reject them unless deliberately mapped and covered by tests.

Validate the complete string before returning a usable sequence. Unsupported
characters produce a normal error containing the character and its UTF-8 byte
offset. Reject invalid UTF-8 and named-key ID collisions. An empty string returns
an empty sequence. Never silently drop characters or fall back to text insertion,
because either behavior changes what page keyboard listeners observe.

Reuse Rod's current keyboard mapping. This is not operating-system layout
detection, IME composition, or arbitrary Unicode typing. Callers should continue
to use `Input` or `InsertText` when they want text insertion. The conversion
guarantees complete validation before typing begins; a later protocol failure
during typing can still leave a partially entered value.

## Related work

[Unicode key printability](../pulls/1220-unicode-key-printability.md) concerns
whether an already registered key emits text.
[Numeric keypad encoding](../issues/1212-numpad-key-encoding.md) concerns key
event metadata. Reuse their corrected behavior where relevant, while keeping
string conversion and validation a separate task.

## Acceptance

- Convert supported letters, digits, punctuation, shifted characters, empty
  strings, and line feeds using the existing key definitions.
- Reject emoji, unmapped characters, invalid UTF-8, and named-key collisions
  such as U+0108 with clear byte offsets and without panics.
- A supported prefix followed by an unsupported character yields an error and
  no usable partial key sequence. No element focus, key event, or text change
  occurs before successful validation in the documented usage.
- Verify keydown/keyup and text behavior in a local browser fixture, including
  an Enter-driven application action and preservation of pre-existing input.
- Demonstrate that Unicode text insertion remains available through the existing
  `Input` API and is not silently substituted for keyboard events.
