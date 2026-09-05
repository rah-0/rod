package testutil

import (
	"reflect"
	"runtime/debug"
	"sync/atomic"
)

// Only marks a suite method as the only method Each should run.
type Only struct{}

// Skip marks a suite method that Each should report as skipped.
type Skip struct{}

// Each runs each exported, non-embedded method of iteratee as a subtest.
// Iteratee may be a struct value or a one-argument function returning one.
func Each(t Testable, iteratee any) int {
	t.Helper()
	factory, suiteType, ok := suiteFactory(t, iteratee)
	if !ok {
		return 0
	}

	methods := suiteMethods(suiteType)
	runner := reflect.ValueOf(t).MethodByName("Run")
	if !runner.IsValid() || runner.Type().NumIn() != 2 || runner.Type().In(1).Kind() != reflect.Func {
		New(t).Fatalf("testutil: %T does not support subtests", t)
		return 0
	}

	callbackType := runner.Type().In(1)
	var count atomic.Int64
	for _, method := range methods {
		method := method
		callback := reflect.MakeFunc(callbackType, func(args []reflect.Value) []reflect.Value {
			child, valid := args[0].Interface().(Testable)
			if !valid {
				New(t).Fatalf("testutil: subtest value %T is not Testable", args[0].Interface())
				return nil
			}

			count.Add(1)
			if methodHasMarker(method, reflect.TypeFor[Skip]()) {
				child.SkipNow()
				return nil
			}

			invokeSuiteMethod(child, factory(args[0]), method)
			return nil
		})

		runner.Call([]reflect.Value{reflect.ValueOf(method.Name), callback})
	}

	return int(count.Load())
}

type suiteFactoryFunc func(reflect.Value) reflect.Value

func suiteFactory(t Testable, iteratee any) (suiteFactoryFunc, reflect.Type, bool) {
	if iteratee == nil {
		New(t).Fatal("testutil: nil suite")
		return nil, nil, false
	}

	value := reflect.ValueOf(iteratee)
	switch value.Kind() {
	case reflect.Struct:
		suiteType := value.Type()
		return func(child reflect.Value) reflect.Value {
			suite := reflect.New(suiteType).Elem()
			suite.Set(value)
			setSuiteHelper(suite, child.Interface().(Testable))
			return suite
		}, suiteType, true

	case reflect.Func:
		typeOf := value.Type()
		if typeOf.NumIn() != 1 || typeOf.NumOut() != 1 {
			New(t).Fatalf("testutil: suite factory %T must accept one argument and return one value", iteratee)
			return nil, nil, false
		}
		return func(child reflect.Value) reflect.Value {
			input := child
			if !input.Type().AssignableTo(typeOf.In(0)) {
				if input.Type().ConvertibleTo(typeOf.In(0)) {
					input = input.Convert(typeOf.In(0))
				} else if typeOf.In(0).Kind() == reflect.Interface && input.Type().Implements(typeOf.In(0)) {
					input = input.Convert(typeOf.In(0))
				} else {
					New(child.Interface().(Testable)).Fatalf(
						"testutil: cannot pass %s to suite factory %T", input.Type(), iteratee,
					)
					return reflect.Zero(typeOf.Out(0))
				}
			}
			return value.Call([]reflect.Value{input})[0]
		}, typeOf.Out(0), true

	default:
		New(t).Fatalf("testutil: suite %T must be a struct or function", iteratee)
		return nil, nil, false
	}
}

func setSuiteHelper(suite reflect.Value, t Testable) {
	if suite.Kind() != reflect.Struct {
		return
	}
	helper := reflect.ValueOf(New(t))
	for i := 0; i < suite.NumField(); i++ {
		fieldInfo := suite.Type().Field(i)
		if !fieldInfo.Anonymous || !suite.Field(i).CanSet() {
			continue
		}

		field := suite.Field(i)
		switch {
		case helper.Type().AssignableTo(field.Type()):
			field.Set(helper)
		case reflect.PointerTo(helper.Type()).AssignableTo(field.Type()):
			copy := New(t)
			field.Set(reflect.ValueOf(&copy))
		}
	}
}

func suiteMethods(suiteType reflect.Type) []reflect.Method {
	embedded := embeddedMethodNames(suiteType)
	methods := make([]reflect.Method, 0, suiteType.NumMethod())
	only := make([]reflect.Method, 0)
	onlyType := reflect.TypeFor[Only]()

	for i := 0; i < suiteType.NumMethod(); i++ {
		method := suiteType.Method(i)
		if embedded[method.Name] {
			continue
		}
		methods = append(methods, method)
		if methodHasMarker(method, onlyType) {
			only = append(only, method)
		}
	}

	if len(only) != 0 {
		return only
	}
	return methods
}

func embeddedMethodNames(suiteType reflect.Type) map[string]bool {
	for suiteType.Kind() == reflect.Pointer {
		suiteType = suiteType.Elem()
	}

	names := map[string]bool{}
	if suiteType.Kind() != reflect.Struct {
		return names
	}
	for i := 0; i < suiteType.NumField(); i++ {
		field := suiteType.Field(i)
		if !field.Anonymous {
			continue
		}
		for j := 0; j < field.Type.NumMethod(); j++ {
			names[field.Type.Method(j).Name] = true
		}
	}
	return names
}

func methodHasMarker(method reflect.Method, marker reflect.Type) bool {
	return method.Type.NumIn() > 1 && method.Type.In(1) == marker
}

func invokeSuiteMethod(t Testable, receiver reflect.Value, method reflect.Method) {
	t.Helper()
	defer func() {
		if value := recover(); value != nil {
			t.Logf("testutil: suite method %s panicked: %v\n%s", method.Name, value, debug.Stack())
			t.Fail()
		}
	}()

	args := make([]reflect.Value, method.Type.NumIn())
	args[0] = receiver
	for i := 1; i < len(args); i++ {
		args[i] = reflect.Zero(method.Type.In(i))
	}
	method.Func.Call(args)
}
