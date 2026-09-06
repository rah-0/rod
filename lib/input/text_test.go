package input_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rah-0/rod/lib/input"
)

func TestKeys(t *testing.T) {
	const text = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 `~!@#$%^&*()-_=+[{]}\\|;:'\",<.>/?"
	keys, err := input.Keys(text + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != len(text)+1 {
		t.Fatalf("got %d keys, want %d", len(keys), len(text)+1)
	}
	for offset, character := range text {
		if got := keys[offset].Info().Key; got != string(character) {
			t.Errorf("at byte %d: got descriptor %q, want %q", offset, got, character)
		}
	}
	if keys[len(text)] != input.Enter {
		t.Errorf("line feed mapped to %v, want Enter", keys[len(text)])
	}
	empty, err := input.Keys("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty string: got %v, %v", empty, err)
	}
}

func TestKeysRejectUnsupportedText(t *testing.T) {
	for _, test := range []struct {
		Text   string
		Offset int
		Want   error
	}{
		{"🦊", 0, input.ErrUnsupportedCharacter},
		{"ok🦊", 2, input.ErrUnsupportedCharacter},
		{"okĈ", 2, input.ErrUnsupportedCharacter},
		{"é", 0, input.ErrUnsupportedCharacter},
		{"�", 0, input.ErrUnsupportedCharacter},
		{"ok\t", 2, input.ErrUnsupportedCharacter},
		{"ok\r", 2, input.ErrUnsupportedCharacter},
		{"ok\x00", 2, input.ErrUnsupportedCharacter},
		{"ok\xff", 2, input.ErrInvalidUTF8},
		{"ok\xc3", 2, input.ErrInvalidUTF8},
		{"\xed\xa0\x80", 0, input.ErrInvalidUTF8},
	} {
		keys, err := input.Keys(test.Text)
		if keys != nil || !errors.Is(err, test.Want) {
			t.Fatalf("%q: got %v, %v; want nil, %v", test.Text, keys, err, test.Want)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("at byte %d", test.Offset)) {
			t.Errorf("%q: missing byte offset: %v", test.Text, err)
		}
	}
}
