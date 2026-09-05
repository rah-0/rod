// Package jsonvalue provides a small, lazy JSON value wrapper.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// Value represents an arbitrary JSON value.
//
// Copies of a Value share their decoded state. Values obtained through Get or
// Gets share underlying maps and slices with their parent.
type Value struct {
	state *state
}

type state struct {
	mu    sync.Mutex
	value any
}

// Query selects a value from the current path segment.
type Query func(any) (value any, found bool)

// New creates a Value from JSON bytes, an io.Reader, another Value, or an
// already decoded Go value.
func New(value any) Value {
	return Value{state: &state{value: value}}
}

// NewFrom creates a Value from a JSON-encoded string.
func NewFrom(value string) Value {
	return New([]byte(value))
}

// MarshalJSON implements json.Marshaler.
func (v Value) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.Val())
}

// UnmarshalJSON implements json.Unmarshaler and keeps the input lazy.
func (v *Value) UnmarshalJSON(data []byte) error {
	*v = New(bytes.Clone(data))
	return nil
}

// Unmarshal decodes the original raw JSON into dst. It must be called before
// an operation that decodes the Value.
func (v Value) Unmarshal(dst any) error {
	if v.state == nil {
		return ErrNoValue
	}

	v.state.mu.Lock()
	defer v.state.mu.Unlock()

	data, ok := rawBytes(v.state.value)
	if !ok {
		return ErrValueDecoded
	}
	return json.Unmarshal(data, dst)
}

// Val returns the decoded Go value. Invalid JSON decodes to nil, matching the
// permissive behavior expected by Rod's protocol helpers.
func (v Value) Val() any {
	if v.state == nil {
		return nil
	}

	v.state.mu.Lock()
	defer v.state.mu.Unlock()

	for {
		nested, ok := v.state.value.(Value)
		if !ok {
			break
		}
		v.state.value = nested.Val()
	}

	switch value := v.state.value.(type) {
	case []byte:
		v.state.value = decodeBytes(value)
	case json.RawMessage:
		v.state.value = decodeBytes(value)
	case io.Reader:
		var decoded any
		if json.NewDecoder(value).Decode(&decoded) != nil {
			decoded = nil
		}
		v.state.value = decoded
	}

	return v.state.value
}

func rawBytes(value any) ([]byte, bool) {
	switch data := value.(type) {
	case []byte:
		return data, true
	case json.RawMessage:
		return data, true
	default:
		return nil, false
	}
}

func decodeBytes(data []byte) any {
	var decoded any
	if json.Unmarshal(data, &decoded) != nil {
		return nil
	}
	return decoded
}

// Raw returns the current underlying value without forcing JSON decoding.
func (v Value) Raw() any {
	if v.state == nil {
		return nil
	}
	v.state.mu.Lock()
	defer v.state.mu.Unlock()
	return v.state.value
}

// JSON formats the decoded value with encoding/json defaults, except that HTML
// escaping is disabled to preserve Rod's existing protocol diagnostics.
func (v Value) JSON(prefix, indent string) string {
	buf := bytes.NewBuffer(nil)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, indent)
	if err := enc.Encode(v.Val()); err != nil {
		return ""
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// String implements fmt.Stringer.
func (v Value) String() string {
	return fmt.Sprint(v.Val())
}

// Get returns the value at a dot-separated path. A missing path yields nil.
func (v Value) Get(path string) Value {
	value, _ := v.Gets(Path(path)...)
	return value
}

// Has reports whether a dot-separated path exists.
func (v Value) Has(path string) bool {
	_, found := v.Gets(Path(path)...)
	return found
}

// Gets returns the value selected by path segments. String segments select map
// keys, integer segments select slice indexes, and Query segments select via a
// caller-provided predicate.
func (v Value) Gets(segments ...any) (Value, bool) {
	for _, segment := range segments {
		var (
			value any
			found bool
		)
		if query, ok := segment.(Query); ok {
			value, found = query(v.Val())
		} else {
			value, found = get(v.Val(), segment)
		}
		if !found {
			return New(nil), false
		}
		v = New(value)
	}
	return v, true
}

func get(value any, segment any) (any, bool) {
	rv := indirect(reflect.ValueOf(value))
	if !rv.IsValid() {
		return nil, false
	}

	switch index := segment.(type) {
	case int:
		if index < 0 || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) || index >= rv.Len() {
			return nil, false
		}
		return rv.Index(index).Interface(), true
	default:
		key := reflect.ValueOf(segment)
		if rv.Kind() != reflect.Map || !key.IsValid() || !key.Type().AssignableTo(rv.Type().Key()) {
			return nil, false
		}
		item := rv.MapIndex(key)
		if !item.IsValid() {
			return nil, false
		}
		return item.Interface(), true
	}
}

func indirect(value reflect.Value) reflect.Value {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}

// Str returns a string value, or the formatted decoded value for other types.
func (v Value) Str() string {
	if value, ok := v.Val().(string); ok {
		return value
	}
	return fmt.Sprint(v.Val())
}

var floatType = reflect.TypeFor[float64]()

// Num returns the value converted to float64, or zero when it is not numeric.
func (v Value) Num() float64 {
	value := reflect.ValueOf(v.Val())
	if value.IsValid() && value.Type().ConvertibleTo(floatType) {
		return value.Convert(floatType).Float()
	}
	return 0
}

// Bool returns the boolean value, or false for another type.
func (v Value) Bool() bool {
	value, _ := v.Val().(bool)
	return value
}

// Nil reports whether the decoded value is nil.
func (v Value) Nil() bool {
	return v.Val() == nil
}

var intType = reflect.TypeFor[int]()

// Int returns the value converted to int, or zero when it is not numeric.
func (v Value) Int() int {
	value := reflect.ValueOf(v.Val())
	if value.IsValid() && value.Type().ConvertibleTo(intType) {
		return int(value.Convert(intType).Int())
	}
	return 0
}

// Map converts a string-keyed map into Values.
func (v Value) Map() map[string]Value {
	value := indirect(reflect.ValueOf(v.Val()))
	result := make(map[string]Value)
	if !value.IsValid() || value.Kind() != reflect.Map || value.Type().Key().Kind() != reflect.String {
		return result
	}
	iter := value.MapRange()
	for iter.Next() {
		result[iter.Key().String()] = New(iter.Value().Interface())
	}
	return result
}

// Arr converts a slice or array into Values.
func (v Value) Arr() []Value {
	value := indirect(reflect.ValueOf(v.Val()))
	if !value.IsValid() || (value.Kind() != reflect.Slice && value.Kind() != reflect.Array) {
		return []Value{}
	}
	result := make([]Value, value.Len())
	for i := range value.Len() {
		result[i] = New(value.Index(i).Interface())
	}
	return result
}

// Join formats array elements and joins them with sep.
func (v Value) Join(sep string) string {
	items := v.Arr()
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = item.Str()
	}
	return strings.Join(parts, sep)
}

// Path parses a dot-separated path into string keys and non-negative indexes.
func Path(path string) []any {
	parts := strings.Split(path, ".")
	segments := make([]any, len(parts))
	for i, part := range parts {
		if isIndex(part) {
			index, err := strconv.Atoi(part)
			if err == nil {
				segments[i] = index
				continue
			}
		}
		segments[i] = part
	}
	return segments
}

func isIndex(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
