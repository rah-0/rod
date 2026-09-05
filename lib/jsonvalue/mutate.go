package jsonvalue

import "reflect"

var anyType = reflect.TypeFor[any]()

// Set replaces the value at a dot-separated path.
func (v *Value) Set(path string, target any) *Value {
	return v.Sets(target, Path(path)...)
}

// Sets replaces the value at path segments, creating maps and slices as needed.
func (v *Value) Sets(target any, segments ...any) *Value {
	if v.state == nil {
		*v = New(nil)
	}

	if len(segments) == 0 {
		v.state.mu.Lock()
		v.state.value = target
		v.state.mu.Unlock()
		return v
	}

	next, changed := setAt(reflect.ValueOf(v.Val()), segments, target)
	if !changed {
		return v
	}

	v.state.mu.Lock()
	v.state.value = valueInterface(next)
	v.state.mu.Unlock()
	return v
}

func setAt(value reflect.Value, segments []any, target any) (reflect.Value, bool) {
	if len(segments) == 0 {
		return reflect.ValueOf(target), true
	}

	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			value = reflect.Value{}
			break
		}
		value = value.Elem()
	}

	if value.IsValid() && value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return setAt(reflect.Value{}, segments, target)
		}

		next, changed := setAt(value.Elem(), segments, target)
		if !changed {
			return value, false
		}
		if assignValue(value.Elem(), next) {
			return value, true
		}
		return next, true
	}

	switch segment := segments[0].(type) {
	case int:
		return setIndex(value, segment, segments[1:], target)
	default:
		return setKey(value, reflect.ValueOf(segment), segments[1:], target)
	}
}

func setIndex(value reflect.Value, index int, segments []any, target any) (reflect.Value, bool) {
	if index < 0 {
		return value, false
	}

	if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
		if index < value.Len() {
			next, changed := setAt(value.Index(index), segments, target)
			if !changed {
				return value, false
			}
			if assignValue(value.Index(index), next) {
				return value, true
			}

			copy := cloneSequence(value)
			if assignValue(copy.Index(index), next) {
				return copy, true
			}
			return setAnyIndex(value, index, next), true
		}

		if value.Kind() == reflect.Slice {
			expanded := reflect.MakeSlice(value.Type(), index+1, index+1)
			reflect.Copy(expanded, value)
			next, changed := setAt(expanded.Index(index), segments, target)
			if !changed {
				return value, false
			}
			if assignValue(expanded.Index(index), next) {
				return expanded, true
			}
			return setAnyIndex(value, index, next), true
		}
	}

	next, changed := setAt(reflect.Value{}, segments, target)
	if !changed {
		return value, false
	}
	return setAnyIndex(value, index, next), true
}

func setAnyIndex(value reflect.Value, index int, target reflect.Value) reflect.Value {
	length := index + 1
	if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
		length = max(length, value.Len())
	}

	next := reflect.MakeSlice(reflect.SliceOf(anyType), length, length)
	if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
		for i := range value.Len() {
			item, _ := valueForType(anyType, value.Index(i))
			next.Index(i).Set(item)
		}
	}
	item, _ := valueForType(anyType, target)
	next.Index(index).Set(item)
	return next
}

func cloneSequence(value reflect.Value) reflect.Value {
	if value.Kind() == reflect.Slice {
		clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		reflect.Copy(clone, value)
		return clone
	}

	clone := reflect.New(value.Type()).Elem()
	clone.Set(value)
	return clone
}

func setKey(value, key reflect.Value, segments []any, target any) (reflect.Value, bool) {
	if !key.IsValid() || !key.Type().Comparable() {
		return value, false
	}

	if value.IsValid() && value.Kind() == reflect.Map && key.Type().AssignableTo(value.Type().Key()) {
		next, changed := setAt(value.MapIndex(key), segments, target)
		if !changed {
			return value, false
		}
		item, assignable := valueForType(value.Type().Elem(), next)
		if assignable {
			if value.IsNil() {
				value = reflect.MakeMap(value.Type())
			}
			value.SetMapIndex(key, item)
			return value, true
		}

		generic := mapWithAnyValues(value)
		item, _ = valueForType(anyType, next)
		generic.SetMapIndex(key, item)
		return generic, true
	}

	next, changed := setAt(reflect.Value{}, segments, target)
	if !changed {
		return value, false
	}
	generic := reflect.MakeMap(reflect.MapOf(key.Type(), anyType))
	item, _ := valueForType(anyType, next)
	generic.SetMapIndex(key, item)
	return generic, true
}

func mapWithAnyValues(value reflect.Value) reflect.Value {
	next := reflect.MakeMapWithSize(reflect.MapOf(value.Type().Key(), anyType), value.Len()+1)
	iter := value.MapRange()
	for iter.Next() {
		item, _ := valueForType(anyType, iter.Value())
		next.SetMapIndex(iter.Key(), item)
	}
	return next
}

func assignValue(dst, src reflect.Value) bool {
	if !dst.IsValid() || !dst.CanSet() {
		return false
	}
	value, ok := valueForType(dst.Type(), src)
	if !ok {
		return false
	}
	dst.Set(value)
	return true
}

func valueForType(target reflect.Type, value reflect.Value) (reflect.Value, bool) {
	if !value.IsValid() {
		switch target.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			return reflect.Zero(target), true
		default:
			return reflect.Value{}, false
		}
	}
	if value.Type().AssignableTo(target) {
		return value, true
	}
	return reflect.Value{}, false
}

func valueInterface(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}
	return value.Interface()
}

// Del removes the value at a dot-separated path.
func (v *Value) Del(path string) *Value {
	v.Dels(Path(path)...)
	return v
}

// Dels removes the value at path segments and reports whether it existed.
func (v *Value) Dels(segments ...any) bool {
	if len(segments) == 0 {
		v.state = nil
		return true
	}

	parent, found := v.Gets(segments[:len(segments)-1]...)
	if !found {
		return false
	}
	parentValue := indirect(reflect.ValueOf(parent.Val()))
	if !parentValue.IsValid() {
		return false
	}

	last := segments[len(segments)-1]
	switch index := last.(type) {
	case int:
		if parentValue.Kind() != reflect.Slice || index < 0 || index >= parentValue.Len() {
			return false
		}
		next := reflect.AppendSlice(parentValue.Slice(0, index), parentValue.Slice(index+1, parentValue.Len()))
		v.Sets(next.Interface(), segments[:len(segments)-1]...)
		return true
	default:
		key := reflect.ValueOf(last)
		if parentValue.Kind() != reflect.Map || !key.IsValid() || !key.Type().AssignableTo(parentValue.Type().Key()) {
			return false
		}
		if !parentValue.MapIndex(key).IsValid() {
			return false
		}
		parentValue.SetMapIndex(key, reflect.Value{})
		return true
	}
}
