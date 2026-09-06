package jsonvalue

import (
	"encoding/json"
	"testing"
)

func TestIsZeroDistinguishesNull(t *testing.T) {
	for _, tc := range []struct {
		value Value
		want  string
	}{
		{Value{}, `{}`},
		{New(nil), `{"value":null}`},
		{NewFrom("null"), `{"value":null}`},
		{New(false), `{"value":false}`},
		{New(0), `{"value":0}`},
	} {
		data, err := json.Marshal(struct {
			Value Value `json:"value,omitzero"`
		}{tc.value})
		if err != nil || string(data) != tc.want {
			t.Fatalf("presence encoding = %s, %v; want %s", data, err, tc.want)
		}
	}
}
