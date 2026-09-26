package proto

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// ErrMissingField matches every [MissingFieldError] with [errors.Is].
var ErrMissingField = errors.New("proto: missing required field")

// MissingFieldError reports decoded protocol data that lacks a field the
// protocol requires, or has null for it. [Unmarshal] and the generated Call
// methods return it.
type MissingFieldError struct {
	// Type is the name of the decoded Go type, such as "RuntimeEvaluateResult".
	Type string
	// Path is the JSON path of the missing field inside Type, such as
	// "result" or "root.children[2].nodeId". A path that ends in an index,
	// such as "targetInfos[1]", refers to a null array entry.
	Path string
}

func (e *MissingFieldError) Error() string {
	return "proto: " + e.Type + ": " + e.Path + " is missing or null"
}

// Is reports whether target is [ErrMissingField].
func (e *MissingFieldError) Is(target error) bool { return target == ErrMissingField }

// Decoding selects how [Decoding.Unmarshal] treats protocol data that lacks
// fields the protocol requires. The zero value is [DecodeStrict].
type Decoding uint8

const (
	// DecodeStrict returns a [*MissingFieldError] for a command result or event
	// that lacks a field the protocol requires, as described on
	// [Decoding.Unmarshal].
	DecodeStrict Decoding = iota

	// DecodeLenient accepts command results and events that lack required
	// fields, have null for them, or have null array entries: such fields and
	// entries keep their zero value, which is nil for objects and arrays. It
	// is meant for endpoints that do not send complete protocol data, such as
	// other implementations of the protocol.
	//
	// The decoded values then do not necessarily hold what the protocol
	// requires. Code that reads them can dereference a nil object and panic,
	// or act on a zero value that the endpoint never sent. Rod's methods check
	// the objects, arrays and object entries that they use, and identifiers
	// such as the session ID of an attached target, and return a
	// [*MissingFieldError] for a missing one. They act on other zero values,
	// and the values that they return can hold nil objects.
	DecodeLenient
)

// Decodable is implemented by a [Client] that selects the [Decoding] of the
// command results that the generated Call methods decode. Results for other
// clients use [DecodeStrict].
type Decodable interface {
	GetDecoding() Decoding
}

// Unmarshal decodes protocol data into v with [DecodeStrict]. See
// [Decoding.Unmarshal].
func Unmarshal(data []byte, v any) error {
	return DecodeStrict.Unmarshal(data, v)
}

// Unmarshal decodes a JSON protocol value into v, which must be a non-nil
// pointer.
//
// Command results, events, and the protocol objects they contain are decoded
// by generated code in one pass. With [DecodeStrict], Unmarshal returns a
// [*MissingFieldError] when, at any depth:
//   - a member that the protocol requires is missing or null;
//   - an array contains null.
//
// Null is accepted for jsonvalue.Value, where it is a value, and for float64
// numbers, which Chrome sends as null when JSON cannot represent them, such as
// infinity; such numbers keep their zero value. Members whose generated
// documentation marks them "(experimental)", "(optional in older browsers)"
// or "Deprecated:" are not required: browsers older than the schema can lack
// experimental and recently added members, and newer ones can drop deprecated
// members. With [DecodeLenient], missing members keep their zero value, and
// null objects and array entries are nil.
//
// Both modes decode as [json.Unmarshal] does, with four exceptions: member
// names must match the protocol's names exactly, including case; a JSON array
// replaces the previous content of a slice; binary fields accept only base64
// strings; and decoding stops at the first error, where [json.Unmarshal]
// continues after a value that does not match its Go type. Unknown members
// are ignored. A later duplicate member is decoded into the value of the
// earlier one, as [json.Unmarshal] does: scalars and arrays are replaced, and
// objects and maps are merged; with [DecodeStrict], each occurrence of an
// object member must itself have every required member. Null leaves
// an optional value unchanged or sets it to nil when its type can be nil, and
// invalid UTF-8 in strings and map keys is replaced with the Unicode
// replacement character. A value that does not match the Go type returns the
// [*json.UnmarshalTypeError] of [json.Unmarshal], except that Field writes
// array indexes in brackets, such as "node.children[0].nodeType", and invalid
// JSON returns a [*json.SyntaxError]. When decoding fails, v keeps the members
// decoded before the failure. Values of other types, such as command
// parameters, are decoded with [json.Unmarshal] and are not checked.
func (mode Decoding) Unmarshal(data []byte, v any) error {
	target, ok := v.(decodable)
	// A type of another package that embeds a generated type has its method
	// too, but its embedded pointer can be nil.
	if value := reflect.ValueOf(v); !ok || value.IsNil() || value.Type().Elem().PkgPath() != packagePath {
		return json.Unmarshal(data, v)
	}
	d := decoders.Get().(*decoder)
	d.reset(data, mode == DecodeLenient)
	err := target.decodeJSON(d)
	if err == nil {
		err = d.end()
	}
	d.reset(nil, false)
	decoders.Put(d)
	if err != nil {
		return decodeFailure(data, v, err)
	}
	return nil
}

// decodable is implemented by the generated types of command results, events
// and the objects they contain.
type decodable interface {
	// decodeJSON decodes the next value, a JSON object or null.
	decodeJSON(d *decoder) error
}

var decoders = sync.Pool{New: func() any { return new(decoder) }}

var packagePath = reflect.TypeFor[decoder]().PkgPath()

// decoder is the state of one decoding pass.
type decoder struct {
	dec     jsontext.Decoder
	input   bytes.Buffer
	lenient bool
	name    []byte // unquoted string with escapes
	strings stringCache
}

// The options keep the encoding/json treatment of duplicate names and invalid UTF-8.
var decoderOptions = []jsontext.Options{jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)}

// reset prepares d to decode data, parsing it in place. A nil data releases
// the previous input, and a large buffer for unquoted strings, so that pooled
// decoders do not keep them.
func (d *decoder) reset(data []byte, lenient bool) {
	d.input = *bytes.NewBuffer(data)
	d.dec.Reset(&d.input, decoderOptions...)
	d.lenient = lenient
	if data == nil && cap(d.name) > 4<<10 {
		d.name = nil
	}
}

// end reports an error unless the input ends after the decoded value.
func (d *decoder) end() error {
	if _, err := d.dec.ReadToken(); err != io.EOF {
		if err == nil {
			err = errTrailingData
		}
		return err
	}
	return nil
}

var errTrailingData = errors.New("proto: data after the top-level value")

// decodeFailure converts an error of the generated code to the error that
// [Decoding.Unmarshal] returns.
func decodeFailure(data []byte, v any, err error) error {
	// Report invalid JSON as encoding/json does, which checks the whole input
	// first, even when decoding stopped at an earlier error.
	var raw json.RawMessage
	if syntax := json.Unmarshal(data, &raw); syntax != nil {
		return syntax
	}
	failure, ok := err.(*decodeError)
	if !ok {
		return err
	}
	typ := reflect.TypeOf(v).Elem()
	path := failure.path()
	if failure.missing {
		return &MissingFieldError{Type: typ.Name(), Path: path}
	}
	typeErr := &json.UnmarshalTypeError{Value: failure.value, Type: failure.target, Offset: failure.offset, Err: failure.err}
	if typeErr.Type == nil {
		typeErr.Type = typ
	}
	// As with encoding/json, an error for the decoded value itself names no field.
	if path != "" {
		typeErr.Struct, typeErr.Field = typ.Name(), path
	}
	return typeErr
}

// decodeError is a failure of the generated code with the JSON path where it
// happened. The path is recorded innermost first while the error returns, so
// only a failure allocates.
type decodeError struct {
	missing bool // a missing or null required value
	// For a value of the wrong JSON type:
	value  string       // the JSON value, such as "string" or "number 1.5"
	target reflect.Type // the Go type, nil for the decoded struct itself
	offset int64
	err    error // the underlying error, such as a base64 error

	elements []pathElement
}

type pathElement struct {
	name  string // "" for an array entry
	index int
}

func (e *decodeError) Error() string {
	if e.missing {
		return "proto: " + e.path() + " is missing or null"
	}
	return "proto: cannot decode " + e.value + " at " + e.path()
}

func (e *decodeError) path() string {
	var path strings.Builder
	for i := len(e.elements) - 1; i >= 0; i-- {
		element := e.elements[i]
		if element.name == "" {
			path.WriteByte('[')
			path.WriteString(strconv.Itoa(element.index))
			path.WriteByte(']')
			continue
		}
		if path.Len() != 0 {
			path.WriteByte('.')
		}
		path.WriteString(element.name)
	}
	return path.String()
}

// inField records that err happened in the member name. Other errors, such as
// syntax errors, are returned unchanged.
func inField(err error, name string) error {
	if err != nil {
		return addPath(err, pathElement{name: name})
	}
	return nil
}

// inEntry records that err happened in entry index of an array.
func inEntry(err error, index int) error {
	return addPath(err, pathElement{index: index})
}

// addPath is not inlined, which keeps the error path out of every member of
// the generated code.
//
//go:noinline
func addPath(err error, element pathElement) error {
	if failure, ok := err.(*decodeError); ok {
		failure.elements = append(failure.elements, element)
	}
	return err
}

// missingMember reports the first required member whose bit is not in seen.
// names lists the required members in bit order, separated by spaces.
func missingMember(seen uint64, names string) error {
	for i := 0; ; i++ {
		name, rest, _ := strings.Cut(names, " ")
		if seen&(1<<i) == 0 || rest == "" {
			return &decodeError{missing: true, elements: []pathElement{{name: name}}}
		}
		names = rest
	}
}
