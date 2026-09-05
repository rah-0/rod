package jsonvalue

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReadAndMarshal(t *testing.T) {
	t.Parallel()

	value := NewFrom(`{"items":[{"name":"rod","enabled":true}],"number":2.5}`)
	if got := value.Get("items.0.name").Str(); got != "rod" {
		t.Fatalf("name = %q", got)
	}
	if !value.Get("items.0.enabled").Bool() || value.Get("missing").Val() != nil {
		t.Fatal("boolean or missing path mismatch")
	}
	if got := value.Get("number").Num(); got != 2.5 {
		t.Fatalf("number = %v", got)
	}
	if got := value.JSON("", ""); got != `{"items":[{"enabled":true,"name":"rod"}],"number":2.5}` {
		t.Fatalf("json = %s", got)
	}

	encoded, err := json.Marshal(value)
	if err != nil || !strings.Contains(string(encoded), `"items"`) {
		t.Fatalf("marshal = %s, %v", encoded, err)
	}
}

func TestMutation(t *testing.T) {
	t.Parallel()

	value := NewFrom(`{"items":[{"name":"rod"}]}`)
	value.Set("items.0.name", "rah-0")
	value.Set("items.0.enabled", true)
	if got := value.Get("items.0.name").Str(); got != "rah-0" {
		t.Fatalf("name = %q", got)
	}
	if !value.Del("items.0.enabled").Get("items.0.enabled").Nil() {
		t.Fatal("deleted value is still present")
	}
	if value.Dels("items", -1) {
		t.Fatal("negative index was deleted")
	}
}

func TestMutationOfTypedContainers(t *testing.T) {
	t.Parallel()

	slice := New([]string{"first"})
	slice.Set("2", "third")
	if got, ok := slice.Val().([]string); !ok || !reflect.DeepEqual(got, []string{"first", "", "third"}) {
		t.Fatalf("expanded typed slice = %#v", slice.Val())
	}

	slice.Set("0.name", "rod")
	if got := slice.Get("0.name").Str(); got != "rod" {
		t.Fatalf("nested typed slice value = %q", got)
	}
	if got := slice.Get("2").Str(); got != "third" {
		t.Fatalf("preserved typed slice value = %q", got)
	}

	object := New(map[string]int{"count": 1})
	object.Set("name", "rod")
	if got := object.Get("count").Int(); got != 1 {
		t.Fatalf("preserved typed map value = %d", got)
	}
	if got := object.Get("name").Str(); got != "rod" {
		t.Fatalf("new typed map value = %q", got)
	}

	nested := New(map[string]string{"keep": "yes"})
	nested.Set("settings.enabled", true)
	if got := nested.Get("keep").Str(); got != "yes" {
		t.Fatalf("preserved nested map value = %q", got)
	}
	if !nested.Get("settings.enabled").Bool() {
		t.Fatal("nested typed map value was not set")
	}
}

func TestSetNullKeepsMapEntry(t *testing.T) {
	t.Parallel()

	values := []Value{
		NewFrom(`{"key":"value"}`),
		New(map[string]string{"key": "value"}),
	}
	for i := range values {
		value := &values[i]
		value.Set("key", nil)
		if !value.Has("key") {
			t.Fatal("setting null deleted the map entry")
		}
		if !value.Get("key").Nil() {
			t.Fatalf("map entry = %#v, want nil", value.Get("key").Val())
		}
		if got := value.JSON("", ""); got != `{"key":null}` {
			t.Fatalf("json = %s", got)
		}
	}
}

func TestRawUnmarshalContract(t *testing.T) {
	t.Parallel()

	value := New([]byte(`{"n":1}`))
	var dst struct {
		N int `json:"n"`
	}
	if err := value.Unmarshal(&dst); err != nil || dst.N != 1 {
		t.Fatalf("unmarshal: %+v, %v", dst, err)
	}
	_ = value.Val()
	if err := value.Unmarshal(&dst); !errors.Is(err, ErrValueDecoded) {
		t.Fatalf("decoded error = %v", err)
	}
	if err := (Value{}).Unmarshal(&dst); !errors.Is(err, ErrNoValue) {
		t.Fatalf("empty error = %v", err)
	}
}
