package testutil

import (
	"errors"
	"math"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var float64Type = reflect.TypeFor[float64]()

// Eq asserts that left and right have equal values.
func (g G) Eq(left, right any) {
	g.Helper()
	if equal(left, right) {
		return
	}
	g.failf("values are not equal:\nleft:  %#v\nright: %#v", left, right)
}

// Neq asserts that left and right have different values.
func (g G) Neq(left, right any) {
	g.Helper()
	if !equal(left, right) {
		return
	}
	g.failf("values are unexpectedly equal: %#v", left)
}

// Gt asserts that left is greater than right.
func (g G) Gt(left, right any) {
	g.Helper()
	g.assertOrder("greater than", left, right, func(c float64) bool { return c > 0 })
}

// Gte asserts that left is greater than or equal to right.
func (g G) Gte(left, right any) {
	g.Helper()
	g.assertOrder("greater than or equal to", left, right, func(c float64) bool { return c >= 0 })
}

// Lt asserts that left is less than right.
func (g G) Lt(left, right any) {
	g.Helper()
	g.assertOrder("less than", left, right, func(c float64) bool { return c < 0 })
}

// Lte asserts that left is less than or equal to right.
func (g G) Lte(left, right any) {
	g.Helper()
	g.assertOrder("less than or equal to", left, right, func(c float64) bool { return c <= 0 })
}

// InDelta asserts that left and right differ by no more than delta.
func (g G) InDelta(left, right any, delta float64) {
	g.Helper()
	comparison, ok := compare(left, right)
	if ok && math.Abs(comparison) <= delta {
		return
	}
	if !ok {
		g.failf("values are not orderable: %#v and %#v", left, right)
		return
	}
	g.failf("difference between %#v and %#v exceeds %v", left, right, delta)
}

// True asserts that value is true.
func (g G) True(value bool) {
	g.Helper()
	if !value {
		g.failf("expected true")
	}
}

// False asserts that value is false.
func (g G) False(value bool) {
	g.Helper()
	if value {
		g.failf("expected false")
	}
}

// Nil asserts that the last argument is nil, including a typed nil.
func (g G) Nil(args ...any) {
	g.Helper()
	value, ok := last(args)
	if ok && isNil(value) {
		return
	}
	if !ok {
		g.failf("Nil requires at least one argument")
		return
	}
	g.failf("expected nil, got %#v", value)
}

// NotNil asserts that the last argument is not nil.
func (g G) NotNil(args ...any) {
	g.Helper()
	value, ok := last(args)
	if ok && !isNil(value) {
		return
	}
	if !ok {
		g.failf("NotNil requires at least one argument")
		return
	}
	g.failf("expected a non-nil value")
}

// Zero asserts that value is its type's zero value.
func (g G) Zero(value any) {
	g.Helper()
	if value == nil || reflect.ValueOf(value).IsZero() {
		return
	}
	g.failf("expected zero value, got %#v", value)
}

// Regex asserts that str matches pattern.
func (g G) Regex(pattern, str string) {
	g.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		g.failf("invalid regular expression %q: %v", pattern, err)
		return
	}
	if !re.MatchString(str) {
		g.failf("%q does not match %q", str, pattern)
	}
}

// Has asserts that container contains item.
func (g G) Has(container, item any) {
	g.Helper()
	if contains(container, item) {
		return
	}
	g.failf("%#v does not contain %#v", container, item)
}

// Len asserts that value has the expected length.
func (g G) Len(value any, expected int) {
	g.Helper()
	v := reflect.ValueOf(value)
	if !v.IsValid() || !hasLength(v.Kind()) {
		g.failf("value of type %T has no length", value)
		return
	}
	if actual := v.Len(); actual != expected {
		g.failf("unexpected length: got %d, want %d for %#v", actual, expected, value)
	}
}

// Err asserts that the last argument is a non-nil error.
func (g G) Err(args ...any) {
	g.Helper()
	value, ok := last(args)
	if ok {
		if err, isError := value.(error); isError && err != nil {
			return
		}
	}
	if !ok {
		g.failf("Err requires at least one argument")
		return
	}
	g.failf("expected an error, got %#v", value)
}

// E stops the test when the last argument is non-nil.
func (g G) E(args ...any) {
	g.Helper()
	value, ok := last(args)
	if ok && isNil(value) {
		return
	}
	if !ok {
		g.Fatalf("E requires at least one argument")
		return
	}
	g.Fatalf("unexpected non-nil value: %#v", value)
}

// Panic runs fn and returns its panic value. It fails if fn does not panic.
func (g G) Panic(fn func()) (value any) {
	g.Helper()
	didPanic := true
	defer func() {
		value = recover()
		if !didPanic {
			g.failf("expected panic")
		}
	}()
	fn()
	didPanic = false
	return nil
}

// Is asserts that errors are in the same chain or values have the same kind.
func (g G) Is(left, right any) {
	g.Helper()
	if left == nil && right == nil {
		return
	}
	if leftErr, ok := left.(error); ok {
		if rightErr, ok := right.(error); ok && errors.Is(leftErr, rightErr) {
			return
		}
		g.failf("error %#v does not match %#v", left, right)
		return
	}
	if left != nil && right != nil && reflect.TypeOf(left).Kind() == reflect.TypeOf(right).Kind() {
		return
	}
	g.failf("values have different kinds: %T and %T", left, right)
}

// Count returns a function that must be called expected times before cleanup.
func (g G) Count(expected int) func() {
	g.Helper()
	var count atomic.Int64
	g.Cleanup(func() {
		if actual := int(count.Load()); actual != expected {
			g.failf("unexpected call count: got %d, want %d", actual, expected)
		}
	})
	return func() {
		count.Add(1)
	}
}

func (g G) assertOrder(description string, left, right any, accept func(float64) bool) {
	comparison, ok := compare(left, right)
	if !ok {
		g.failf("values are not orderable: %#v and %#v", left, right)
		return
	}
	if !accept(comparison) {
		g.failf("expected %#v to be %s %#v", left, description, right)
	}
}

func (g G) failf(format string, args ...any) {
	g.Helper()
	g.Logf(format, args...)
	g.Fail()
}

func equal(left, right any) bool {
	if isNil(left) && isNil(right) {
		return true
	}
	if reflect.DeepEqual(left, right) {
		return true
	}
	comparison, ok := compare(left, right)
	return ok && comparison == 0
}

func compare(left, right any) (float64, bool) {
	if left == nil || right == nil {
		return 0, false
	}

	lv := indirect(reflect.ValueOf(left))
	rv := indirect(reflect.ValueOf(right))
	if !lv.IsValid() || !rv.IsValid() {
		return 0, false
	}

	if lv.Type().ConvertibleTo(float64Type) && rv.Type().ConvertibleTo(float64Type) {
		return lv.Convert(float64Type).Float() - rv.Convert(float64Type).Float(), true
	}

	if lt, ok := lv.Interface().(time.Time); ok {
		if rt, ok := rv.Interface().(time.Time); ok {
			return float64(lt.Sub(rt)), true
		}
	}

	if lv.Kind() == reflect.String && rv.Kind() == reflect.String {
		return float64(strings.Compare(lv.String(), rv.String())), true
	}
	if lv.Kind() == reflect.Bool && rv.Kind() == reflect.Bool {
		switch {
		case lv.Bool() == rv.Bool():
			return 0, true
		case lv.Bool():
			return 1, true
		default:
			return -1, true
		}
	}

	return 0, false
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

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func last(args []any) (any, bool) {
	if len(args) == 0 {
		return nil, false
	}
	return args[len(args)-1], true
}

func contains(container, item any) bool {
	v := indirect(reflect.ValueOf(container))
	if !v.IsValid() {
		return false
	}

	switch v.Kind() {
	case reflect.String:
		switch item := item.(type) {
		case string:
			return strings.Contains(v.String(), item)
		case []byte:
			return strings.Contains(v.String(), string(item))
		case rune:
			return strings.ContainsRune(v.String(), item)
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			switch item := item.(type) {
			case string:
				return strings.Contains(stringBytes(v), item)
			case []byte:
				return strings.Contains(stringBytes(v), string(item))
			case rune:
				return strings.ContainsRune(stringBytes(v), item)
			}
		}
		for i := range v.Len() {
			if equal(v.Index(i).Interface(), item) {
				return true
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if equal(iter.Value().Interface(), item) {
				return true
			}
		}
	}
	return false
}

func stringBytes(value reflect.Value) string {
	buf := make([]byte, value.Len())
	for i := range value.Len() {
		buf[i] = byte(value.Index(i).Uint())
	}
	return string(buf)
}

func hasLength(kind reflect.Kind) bool {
	switch kind {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return true
	default:
		return false
	}
}
