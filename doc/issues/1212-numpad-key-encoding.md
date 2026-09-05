# Encode numeric keypad input as digits instead of navigation keys

- Priority: P1
- Source: [#1212](https://github.com/go-rod/rod/issues/1212).
- Reviewed: 2026-09-05.
- Validation: reproduced in a textarea fixture with Chrome 152; typing `+212635413216` through `input.Numpad*` produces `+353`, exactly as reported upstream.

[lib/input/keymap.go](../../lib/input/keymap.go) assigns navigation virtual-key codes to printable numeric keypad keys: `Numpad1` has 35 (End), `Numpad2` has 40 (Down), and similar mismatches affect the other digits and decimal. [Key.Encode](../../lib/input/keyboard.go) forwards these codes unchanged alongside printable key/text values. The [Windows virtual-key table](https://learn.microsoft.com/en-us/windows/win32/inputdev/virtual-key-codes) defines numeric keypad digits as `0x60` through `0x69`. Existing encoding coverage currently asserts the erroneous value 35 for `Numpad1`.

Correct the dispatched keypad virtual-key codes and cover their browser behavior. Preserve the exported `input.Key` identities: `AddKey` currently derives non-ASCII key values from the key code and location, so changing the table alone also changes those public values. Keep regular number-row keys and explicit navigation keys working.

Acceptance criteria:

- Numeric keypad digits and decimal produce the intended text in a local input/textarea without moving the caret or deleting text.
- Typing the reported sequence returns the complete original sequence.
- Unit tests verify numeric keypad event key/code/text and virtual-key values; existing `input.Key` identities remain stable.
- Number-row, arithmetic keypad, modifier, and explicit navigation key behavior remains covered.
