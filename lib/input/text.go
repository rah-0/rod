package input

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

var (
	// ErrUnsupportedCharacter means a character has no supported text key mapping.
	ErrUnsupportedCharacter = errors.New("character has no keyboard mapping")

	// ErrInvalidUTF8 means the text contains an invalid UTF-8 encoding.
	ErrInvalidUTF8 = errors.New("invalid UTF-8")
)

// Keys converts text to keys for Keyboard.Type or Element.Type. It validates the
// entire string before returning any keys, so callers can validate before
// focusing an element or changing its value. Errors include the UTF-8 byte
// offset; no partial sequence is returned.
//
// Keys uses the existing keyboard map, including shifted characters, and maps
// line feed to Enter. Tab, carriage return, invalid UTF-8, and unmapped
// characters are rejected. An empty string returns an empty sequence.
// This is not layout detection or arbitrary Unicode input; use Element.Input
// or Page.InsertText for text insertion. A later typing error can still leave
// partially entered text.
func Keys(text string) ([]Key, error) {
	var keys []Key
	for offset, character := range text {
		if character == utf8.RuneError {
			_, size := utf8.DecodeRuneInString(text[offset:])
			if size == 1 {
				return nil, fmt.Errorf("%w: byte 0x%02x at byte %d", ErrInvalidUTF8, text[offset], offset)
			}
		}
		if character == '\n' {
			keys = append(keys, Enter)
			continue
		}
		key := Key(character)
		info, exists := keyMap[key]
		if !exists {
			info, exists = keyMapShifted[key]
		}
		// Named keys share the integer space with characters. Match the actual
		// descriptor to reject collisions such as U+0108 and Backspace.
		if !exists || info.Key != string(character) || character == '\t' || character == '\r' {
			return nil, fmt.Errorf("%w: %q at byte %d", ErrUnsupportedCharacter, character, offset)
		}
		keys = append(keys, key)
	}
	return keys, nil
}
