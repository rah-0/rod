package jsonvalue

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func FuzzValue(f *testing.F) {
	for _, seed := range []string{
		`null`, `false`, `0`, `""`, `[]`, `{}`, `{"items":[null,false,0,"",{}]}`,
		`{"key":1,"key":2}`, `"\ud800"`, `1e1000`, `{`,
	} {
		f.Add(seed, "items.0")
	}
	f.Fuzz(func(t *testing.T, raw, path string) {
		if len(raw) > 4096 || len(path) > 256 {
			t.Skip()
		}
		var want any
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			want = nil
		}
		value := NewFrom(raw)
		if got := value.Val(); !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded value = %#v, want %#v", got, want)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if got := New(encoded).Val(); !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip = %#v, want %#v", got, want)
		}
		selected, found := value.Gets(Path(path)...)
		if found != value.Has(path) || !reflect.DeepEqual(selected.Val(), value.Get(path).Val()) {
			t.Fatal("path lookup methods disagree")
		}
	})
}

func FuzzPath(f *testing.F) {
	for _, seed := range []string{"", ".", "items.0.name", "0.01.-1", "999999999999999999999999"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if len(path) > 256 {
			t.Skip()
		}
		var parts []string
		for _, segment := range Path(path) {
			switch value := segment.(type) {
			case string:
				parts = append(parts, value)
			case int:
				if value < 0 {
					t.Fatalf("negative path index: %d", value)
				}
				parts = append(parts, strconv.Itoa(value))
			default:
				t.Fatalf("unexpected path segment: %T", segment)
			}
		}
		if got := strings.Join(parts, "."); got != path {
			t.Fatalf("path round trip = %q, want %q", got, path)
		}
	})
}
