package proto

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"strconv"
	"unicode/utf8"

	"github.com/rah-0/rod/lib/jsonvalue"
)

// Helpers of the generated decodeJSON methods. Each one decodes the next JSON
// value into dst. When req is true, the value is required: null is rejected
// unless the decoder is lenient. Otherwise null leaves a value that cannot be
// nil unchanged and sets one that can be nil to nil, as encoding/json does.

// object starts decoding a struct. It reports whether the next value is an
// object, whose opening brace it consumes; for null it consumes the null.
func (d *decoder) object() (bool, error) {
	switch d.dec.PeekKind() {
	case '{':
		_, err := d.dec.ReadToken()
		return err == nil, err
	case 'n':
		_, err := d.dec.ReadToken()
		return false, err
	}
	return false, d.mismatch(nil)
}

// member returns the name of the next member of the current object, or false
// after consuming the end of the object. The name is valid until the next read.
// Invalid UTF-8 is kept: such a name matches no protocol member, as with
// encoding/json, which replaces it.
func (d *decoder) member() ([]byte, bool, error) {
	if d.dec.PeekKind() == '}' {
		_, err := d.dec.ReadToken()
		return nil, false, err
	}
	raw, err := d.dec.ReadValue()
	if err != nil {
		return nil, false, err
	}
	name := raw[1 : len(raw)-1]
	if bytes.IndexByte(name, '\\') >= 0 {
		// The decoder has already validated the name.
		d.name, _ = jsontext.AppendUnquote(d.name[:0], raw)
		name = d.name
	}
	return name, true, nil
}

// key returns the next key of a map, or false after consuming the end of the
// object. Unlike member, it replaces invalid UTF-8 as encoding/json does.
func (d *decoder) key() (string, bool, error) {
	if d.dec.PeekKind() == '}' {
		_, err := d.dec.ReadToken()
		return "", false, err
	}
	raw, err := d.dec.ReadValue()
	if err != nil {
		return "", false, err
	}
	return d.text(raw), true, nil
}

// skip skips the value of an unknown member.
func (d *decoder) skip() error {
	return d.dec.SkipValue()
}

// skipObject decodes a struct without members: an object or null.
func (d *decoder) skipObject() error {
	ok, err := d.object()
	for ok && err == nil {
		if _, ok, err = d.member(); ok && err == nil {
			err = d.skip()
		}
	}
	return err
}

// null handles a null value that has been consumed.
func (d *decoder) null(req bool) error {
	if req && !d.lenient {
		return &decodeError{missing: true}
	}
	return nil
}

// errMismatch reports that start found a value of another kind.
var errMismatch = errors.New("proto: unexpected JSON kind")

// start reports whether the next value is an object or array of the given
// kind, without consuming it. It consumes null and returns the error of
// d.null, and returns errMismatch for a value of another kind. It keeps these
// cases out of the helpers that are instantiated for each type.
func (d *decoder) start(kind jsontext.Kind, req bool) (bool, error) {
	switch d.dec.PeekKind() {
	case kind:
		return true, nil
	case 'n':
		if _, err := d.dec.ReadToken(); err != nil {
			return false, err
		}
		return false, d.null(req)
	}
	return false, errMismatch
}

// mismatch consumes a value that target cannot hold and reports it.
func (d *decoder) mismatch(target reflect.Type) error {
	raw, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	return d.mismatchValue(raw, target, nil)
}

// mismatchValue reports the value raw, which has just been read, as
// encoding/json does: the description of a number includes it only when
// target is a number type, and the offset of an array or object is the one
// after its opening bracket.
func (d *decoder) mismatchValue(raw jsontext.Value, target reflect.Type, cause error) error {
	var value string
	offset := d.dec.InputOffset()
	switch raw.Kind() {
	case '"':
		value = "string"
	case '0':
		value = "number"
		if target != nil && reflect.Int <= target.Kind() && target.Kind() <= reflect.Float64 {
			value += " " + string(raw)
		}
	case 't', 'f':
		value = "bool"
	case '[':
		value = "array"
		offset -= int64(len(raw)) - 1
	case '{':
		value = "object"
		offset -= int64(len(raw)) - 1
	default:
		value = raw.Kind().String()
	}
	return &decodeError{value: value, target: target, offset: offset, err: cause}
}

// text returns the string of a raw JSON string.
func (d *decoder) text(raw jsontext.Value) string {
	s := raw[1 : len(raw)-1]
	if bytes.IndexByte(s, '\\') >= 0 || !utf8.Valid(s) {
		// The decoder has validated the string, so the only error is
		// invalid UTF-8, which is replaced as encoding/json does.
		d.name, _ = jsontext.AppendUnquote(d.name[:0], raw)
		s = d.name
	}
	return d.intern(s)
}

// stringCache holds short strings that a pooled decoder has decoded. Protocol
// data repeats many of them, such as node names, attribute names and value
// types, which can then share one allocation.
type stringCache [256]string

// intern returns string(s), from the cache when it holds s.
func (d *decoder) intern(s []byte) string {
	if len(s) < 2 || len(s) > 64 {
		return string(s)
	}
	// Hash the length and up to 8 bytes at each end, which costs the same
	// for every length.
	var head, tail uint64
	for i := range min(len(s), 8) {
		head = head<<8 | uint64(s[i])
		tail = tail<<8 | uint64(s[len(s)-1-i])
	}
	hash := (head*0x9e3779b97f4a7c15 ^ tail*0xc2b2ae3d27d4eb4f ^ uint64(len(s))) >> 56
	if cached := d.strings[hash]; cached == string(s) {
		return cached
	}
	str := string(s)
	d.strings[hash] = str
	return str
}

func decodeString[T ~string](d *decoder, dst *T, req bool) error {
	raw, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	switch raw[0] {
	case '"':
		*dst = T(d.text(raw))
		return nil
	case 'n':
		return d.null(req)
	}
	return d.mismatchValue(raw, reflect.TypeFor[T](), nil)
}

func decodeBool[T ~bool](d *decoder, dst *T, req bool) error {
	raw, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	switch raw[0] {
	case 't':
		*dst = true
		return nil
	case 'f':
		*dst = false
		return nil
	case 'n':
		return d.null(req)
	}
	return d.mismatchValue(raw, reflect.TypeFor[T](), nil)
}

func decodeInt[T ~int](d *decoder, dst *T, req bool) error {
	raw, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	switch raw[0] {
	case 'n':
		return d.null(req)
	case '"', 't', 'f', '[', '{':
		return d.mismatchValue(raw, reflect.TypeFor[T](), nil)
	}
	n, ok := parseInt(raw)
	if !ok {
		return d.mismatchValue(raw, reflect.TypeFor[T](), nil)
	}
	*dst = T(n)
	return nil
}

// parseInt parses a JSON number that must be an integer that fits in an int.
func parseInt(raw []byte) (int, bool) {
	digits := raw
	if len(digits) > 0 && digits[0] == '-' {
		digits = digits[1:]
	}
	if len(digits) == 0 || len(digits) > 18 {
		n, err := strconv.ParseInt(string(raw), 10, strconv.IntSize)
		return int(n), err == nil
	}
	n := 0
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if len(digits) != len(raw) {
		n = -n
	}
	if strconv.IntSize == 32 && int64(n) != int64(int32(n)) {
		return 0, false
	}
	return n, true
}

// decodeFloat accepts null for a required number and leaves dst unchanged:
// Chrome sends null for numbers that JSON cannot represent, such as infinity.
func decodeFloat[T ~float64](d *decoder, dst *T, _ bool) error {
	raw, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	switch raw[0] {
	case 'n':
		return nil
	case '"', 't', 'f', '[', '{':
		return d.mismatchValue(raw, reflect.TypeFor[T](), nil)
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return d.mismatchValue(raw, reflect.TypeFor[T](), nil)
	}
	*dst = T(f)
	return nil
}

// decodePointer decodes an optional number or boolean whose Go type is a pointer.
func decodePointer[T any](d *decoder, dst **T, decode func(*decoder, *T, bool) error) error {
	if d.dec.PeekKind() == 'n' {
		_, err := d.dec.ReadToken()
		*dst = nil
		return err
	}
	if *dst == nil {
		*dst = new(T)
	}
	return decode(d, *dst, false)
}

func decodeBytes[T ~[]byte](d *decoder, dst *T, req bool) error {
	raw, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	switch raw[0] {
	case '"':
	case 'n':
		*dst = nil
		return d.null(req)
	default:
		return d.mismatchValue(raw, reflect.TypeFor[T](), nil)
	}
	encoded := raw[1 : len(raw)-1]
	if bytes.IndexByte(encoded, '\\') >= 0 {
		unquoted, _ := jsontext.AppendUnquote(nil, raw)
		encoded = unquoted
	}
	decoded := make(T, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(decoded, encoded)
	if err != nil {
		return d.mismatchValue(raw, reflect.TypeFor[T](), err)
	}
	*dst = decoded[:n]
	return nil
}

// decodeValue decodes any JSON value, including null, as jsonvalue.Value does.
func decodeValue(d *decoder, dst *jsonvalue.Value, _ bool) error {
	raw, err := d.dec.ReadValue()
	if err != nil {
		return err
	}
	return dst.UnmarshalJSON(raw)
}

// decodeMap decodes a JSON object into a map, adding to an existing map.
func decodeMap[M ~map[string]jsonvalue.Value](d *decoder, dst *M, req bool) error {
	if ok, err := d.start('{', req); !ok {
		if err == errMismatch {
			return d.mismatch(reflect.TypeFor[M]())
		}
		*dst = nil
		return err
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return err
	}
	m := *dst
	if m == nil {
		m = M{}
		*dst = m
	}
	for {
		key, ok, err := d.key()
		if err != nil || !ok {
			return err
		}
		var value jsonvalue.Value
		if err := decodeValue(d, &value, false); err != nil {
			return inField(err, key)
		}
		m[key] = value
	}
}

// decodeObject decodes a protocol object into a new or the existing struct.
func decodeObject[T any, P interface {
	*T
	decodable
}](d *decoder, dst **T, req bool) error {
	if ok, err := d.start('{', req); !ok {
		if err == errMismatch {
			return d.mismatch(reflect.TypeFor[T]())
		}
		*dst = nil
		return err
	}
	if *dst == nil {
		*dst = new(T)
	}
	return P(*dst).decodeJSON(d)
}

// decodeList decodes a JSON array into a new slice. Entries are required.
// The slice type is unnamed, so that the element types of one shape, such as
// all pointers, share one instantiation; the generated code converts named
// slice types.
func decodeList[E any](d *decoder, dst *[]E, req bool, decode func(*decoder, *E, bool) error) error {
	if ok, err := d.start('[', req); !ok {
		if err == errMismatch {
			return d.mismatch(reflect.TypeFor[[]E]())
		}
		*dst = nil
		return err
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return err
	}
	list := []E{}
	for d.dec.PeekKind() != ']' {
		// Decoding into the slice avoids moving each entry to the heap.
		var zero E
		list = append(list, zero)
		if err := decode(d, &list[len(list)-1], true); err != nil {
			*dst = list[:len(list)-1]
			return inEntry(err, len(list)-1)
		}
	}
	*dst = list
	_, err := d.dec.ReadToken()
	return err
}

// decodeSlice decodes a JSON array into a slice of the named type S, such as
// DOMQuad. The helpers below also decode the entries of arrays of arrays, such
// as []DOMQuad.
func decodeSlice[S ~[]E, E any](d *decoder, dst *S, req bool, decode func(*decoder, *E, bool) error) error {
	list := []E(*dst)
	err := decodeList(d, &list, req, decode)
	*dst = S(list)
	if failure, ok := err.(*decodeError); ok && len(failure.elements) == 0 && failure.target == reflect.TypeFor[[]E]() {
		// Report a value that is not an array as a mismatch for S, as
		// encoding/json does, instead of the slice type of decodeList.
		failure.target = reflect.TypeFor[S]()
	}
	return err
}

func decodeStrings[S ~[]E, E ~string](d *decoder, dst *S, req bool) error {
	return decodeSlice(d, dst, req, decodeString[E])
}

func decodeInts[S ~[]E, E ~int](d *decoder, dst *S, req bool) error {
	return decodeSlice(d, dst, req, decodeInt[E])
}

func decodeFloats[S ~[]E, E ~float64](d *decoder, dst *S, req bool) error {
	return decodeSlice(d, dst, req, decodeFloat[E])
}

func decodeObjects[S ~[]*T, T any, P interface {
	*T
	decodable
}](d *decoder, dst *S, req bool) error {
	return decodeSlice(d, dst, req, decodeObject[T, P])
}
