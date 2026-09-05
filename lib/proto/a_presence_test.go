package proto_test

import (
	"encoding/json"
	"testing"

	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
)

func TestJSONValuePresence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value jsonvalue.Value
		want  string
	}{
		{"unset", jsonvalue.Value{}, "null"},
		{"null", jsonvalue.New(nil), "null"},
		{"false", jsonvalue.New(false), "false"},
		{"zero", jsonvalue.New(0), "0"},
		{"empty string", jsonvalue.New(""), `""`},
		{"empty array", jsonvalue.New([]any{}), "[]"},
		{"empty object", jsonvalue.New(map[string]any{}), "{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, value := range []any{
				proto.AccessibilityAXValue{Value: tc.value},
				proto.RuntimeRemoteObject{Value: tc.value},
				proto.RuntimeDeepSerializedValue{Value: tc.value},
				proto.RuntimeCallArgument{Value: tc.value},
			} {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				var object map[string]json.RawMessage
				if err := json.Unmarshal(data, &object); err != nil {
					t.Fatal(err)
				}
				if got, present := object["value"]; !present || string(got) != tc.want {
					t.Errorf("%T value = %s, present=%v; want %s", value, got, present, tc.want)
				}
			}
		})
	}
}

func TestProtocolOptionalPresence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   any
		field   string
		want    string
		present bool
	}{
		{"omitted number", proto.EmulationSetDeviceMetricsOverride{}, "scale", "", false},
		{"explicit zero", proto.EmulationSetDeviceMetricsOverride{Scale: new(float64(0))}, "scale", "0", true},
		{"nil body", proto.FetchFulfillRequest{}, "body", "null", true},
		{"empty body", proto.FetchFulfillRequest{Body: []byte{}}, "body", `""`, true},
		{"unset argument", proto.RuntimeCallArgument{}, "value", "null", true},
		{"object argument", proto.RuntimeCallArgument{ObjectID: "fixture-object"}, "value", "null", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(data, &object); err != nil {
				t.Fatal(err)
			}
			got, present := object[tc.field]
			if present != tc.present || string(got) != tc.want {
				t.Fatalf("%s = %s, present=%v", tc.field, got, present)
			}
		})
	}
}

func TestCommandResponseRouting(t *testing.T) {
	c := &Client{ret: &proto.BrowserGetVersionResult{ProtocolVersion: "1.3", Product: "fixture"}}
	result, err := (proto.BrowserGetVersion{}).Call(c)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProtocolVersion != "1.3" || result.Product != "fixture" {
		t.Fatalf("response was not decoded: %+v", result)
	}
	if c.methodName != "Browser.getVersion" {
		t.Fatalf("method = %q", c.methodName)
	}
}
