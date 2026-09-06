package input_test

import (
	"reflect"
	"testing"

	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/proto"
)

func TestNumericKeypadEncoding(t *testing.T) {
	keys := []input.Key{input.Numpad0, input.Numpad1, input.Numpad2, input.Numpad3,
		input.Numpad4, input.Numpad5, input.Numpad6, input.Numpad7, input.Numpad8, input.Numpad9}
	for digit, key := range keys {
		info := key.Info()
		event := key.Encode(proto.InputDispatchKeyEventTypeKeyDown, 0)
		if info.KeyCode != 96+digit || event.WindowsVirtualKeyCode != 96+digit ||
			event.Text != string(rune('0'+digit)) || event.IsKeypad == nil || !*event.IsKeypad || event.Location != nil {
			t.Fatalf("keypad %d: info=%+v event=%+v", digit, info, event)
		}
	}
	if event := input.NumpadDecimal.Encode(proto.InputDispatchKeyEventTypeKeyDown, 0); event.WindowsVirtualKeyCode != 110 || event.Text != "." {
		t.Fatalf("decimal: %+v", event)
	}
	if input.Digit1.Info().KeyCode != 49 || input.End.Info().KeyCode != 35 {
		t.Fatal("number-row or navigation mapping changed")
	}
}

func TestRegisteredUnicodeEncoding(t *testing.T) {
	for i, text := range []string{"б", "é", "🦊"} {
		key := input.AddKey(text, "", "CustomCharacter", 200+i, 4)
		event := key.Encode(proto.InputDispatchKeyEventTypeKeyDown, 0)
		if !key.Printable() || event.Type != proto.InputDispatchKeyEventTypeKeyDown || event.Text != text || event.UnmodifiedText != text {
			t.Fatalf("registered %q: %+v", text, event)
		}
	}
	if input.ShiftLeft.Printable() || input.ArrowLeft.Printable() {
		t.Fatal("named keys became printable")
	}
}

func TestMacEditingCombinations(t *testing.T) {
	old := input.IsMac
	input.IsMac = true
	defer func() { input.IsMac = old }()
	for _, tc := range []struct {
		key       input.Key
		modifiers int
		commands  []string
	}{
		{input.KeyA, input.ModifierMeta, []string{"selectAll"}},
		{input.KeyC, input.ModifierMeta, []string{"copy"}},
		{input.KeyV, input.ModifierMeta, []string{"paste"}},
		{input.KeyZ, input.ModifierMeta, []string{"undo"}},
		{input.KeyZ, input.ModifierShift | input.ModifierMeta, []string{"redo"}},
		{input.ArrowLeft, input.ModifierShift, []string{"moveLeftAndModifySelection"}},
		{input.KeyB, input.ModifierShift | input.ModifierControl | input.ModifierAlt, []string{"moveWordBackwardAndModifySelection"}},
		{input.Enter, 0, []string{"insertNewline"}},
		{input.NumpadEnter, input.ModifierControl, []string{"insertLineBreak"}},
		{input.NumpadSubtract, input.ModifierMeta, []string{"cancel"}},
	} {
		event := tc.key.Encode(proto.InputDispatchKeyEventTypeKeyDown, tc.modifiers)
		if !reflect.DeepEqual(event.Commands, tc.commands) {
			t.Fatalf("%s modifiers=%d: commands=%v, want %v", tc.key.Info().Code, tc.modifiers, event.Commands, tc.commands)
		}
		if commands := tc.key.Encode(proto.InputDispatchKeyEventTypeKeyUp, tc.modifiers).Commands; len(commands) != 0 {
			t.Fatalf("key-up repeats editing command: %v", commands)
		}
	}
}
