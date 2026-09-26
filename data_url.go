package rod

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidDataURL means a value that should be a data URL cannot be decoded,
// such as a canvas toDataURL result replaced by the page.
var ErrInvalidDataURL = errors.New("invalid data URL")

const asciiWhitespace = "\t\n\f\r "

// decodeDataURL decodes the body of a data URL string with the percent-decoding
// and forgiving-base64 rules of the Fetch data URL processor: a "%" without two
// hex digits stays literal, and a ";base64" media type suffix selects base64,
// which ignores ASCII whitespace and accepts omitted padding. The string is not
// parsed as a URL, so a "#" belongs to the body.
// https://fetch.spec.whatwg.org/#data-url-processor
func decodeDataURL(uri string) ([]byte, error) {
	const scheme = "data:"
	if len(uri) < len(scheme) || !strings.EqualFold(uri[:len(scheme)], scheme) {
		return nil, fmt.Errorf("%w: missing data scheme", ErrInvalidDataURL)
	}
	mediaType, body, found := strings.Cut(uri[len(scheme):], ",")
	if !found {
		return nil, fmt.Errorf("%w: missing comma", ErrInvalidDataURL)
	}
	if strings.IndexByte(body, '%') >= 0 {
		body = percentDecode(body)
	}
	if !isBase64MediaType(mediaType) {
		return []byte(body), nil
	}

	// Padded base64 without spaces, as toDataURL returns, decodes in one pass.
	// Whatever StdEncoding accepts, forgiving-base64 decodes to the same bytes.
	if data, err := base64.StdEncoding.DecodeString(body); err == nil {
		return data, nil
	}
	body = removeASCIIWhitespace(body)
	if len(body)%4 == 0 {
		body = strings.TrimSuffix(body, "=")
		body = strings.TrimSuffix(body, "=")
	}
	data, err := base64.RawStdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidDataURL, err)
	}
	return data, nil
}

// isBase64MediaType reports whether a data URL media type ends with ";base64",
// allowing spaces before the case-insensitive token.
func isBase64MediaType(mediaType string) bool {
	const token = "base64"
	mediaType = strings.Trim(mediaType, asciiWhitespace)
	if len(mediaType) < len(token) || !strings.EqualFold(mediaType[len(mediaType)-len(token):], token) {
		return false
	}
	return strings.HasSuffix(strings.TrimRight(mediaType[:len(mediaType)-len(token)], " "), ";")
}

func removeASCIIWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(asciiWhitespace, s[i]) < 0 {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// percentDecode replaces each "%" followed by two hex digits with that byte and
// keeps any other "%", like the URL Standard's percent-decode.
func percentDecode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, okHi := unhex(s[i+1])
			lo, okLo := unhex(s[i+2])
			if okHi && okLo {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
