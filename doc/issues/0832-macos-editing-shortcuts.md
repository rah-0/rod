# Encode macOS editing commands using the full key combination

Priority: P2. Source: [issue #832](https://github.com/go-rod/rod/issues/832).

Reviewed: 2026-09-05.

## Current evidence

[Key.Encode](../../lib/input/keyboard.go) looks up `macCommands[info.Key]`. The [command table](../../lib/input/mac_comands.go) contains entries such as `Meta+KeyV`, `Meta+KeyA`, and `Shift+ArrowLeft`, so those entries are unreachable. The current [macOS test](../../lib/input/keyboard_test.go) covers only an unmodified arrow key.

Reproduced without a browser by setting the existing `input.IsMac` option and encoding key-down events: Meta+V produces no commands, and Shift+ArrowLeft produces `moveLeft`, losing the selection modifier. The table already defines the expected `paste` and `moveLeftAndModifySelection` commands. Actual system-clipboard behavior was not tested on macOS.

## Scope

Derive command lookup keys from the modifier mask and physical key code in the table's canonical order. Preserve ordinary key encoding and use editing commands only for appropriate event types. Include Enter and keypad cases whose `Key` and `Code` differ. Keep browser-platform selection compatible with the existing `IsMac` mechanism.

## Acceptance

- Pure encoding tests cover copy/paste/select-all/undo/redo, shifted arrows, Control/Alt combinations, Enter, and keypad keys.
- Modified shortcuts select the corresponding table command; ordinary keys retain existing behavior and key-up does not execute an editing action again.
- A macOS browser fixture verifies copying and pasting between inputs, including the paste event, without relying on preexisting clipboard contents.
