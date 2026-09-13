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
		{"unset argument", proto.RuntimeCallArgument{}, "value", "", false},
		{"object argument", proto.RuntimeCallArgument{ObjectID: "fixture-object"}, "value", "", false},
		{"explicit null argument", proto.RuntimeCallArgument{Value: jsonvalue.New(nil)}, "value", "null", true},
		{"explicit false argument", proto.RuntimeCallArgument{Value: jsonvalue.New(false)}, "value", "false", true},
		{"omitted bool", proto.PageCaptureScreenshot{}, "fromSurface", "", false},
		{"explicit false", proto.PageCaptureScreenshot{FromSurface: new(false)}, "fromSurface", "false", true},
		{"explicit true", proto.PageCaptureScreenshot{FromSurface: new(true)}, "fromSurface", "true", true},
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

type blockedURLsWireTestCase struct {
	name    string
	request proto.NetworkSetBlockedURLs
	want    string
}

func TestNetworkSetBlockedURLsPresence(t *testing.T) {
	for _, tc := range []blockedURLsWireTestCase{
		{
			name:    "nil URLs",
			request: proto.NetworkSetBlockedURLs{},
			want:    `{}`,
		},
		{
			name:    "empty URLs clear blocking",
			request: proto.NetworkSetBlockedURLs{Urls: []string{}},
			want:    `{"urls":[]}`,
		},
		{
			name:    "nonempty URLs",
			request: proto.NetworkSetBlockedURLs{Urls: []string{"*.js"}},
			want:    `{"urls":["*.js"]}`,
		},
		{
			name: "URL patterns omit URLs",
			request: proto.NetworkSetBlockedURLs{
				URLPatterns: []*proto.NetworkBlockPattern{{URLPattern: "*://*:*/*.css", Block: true}},
			},
			want: `{"urlPatterns":[{"urlPattern":"*://*:*/*.css","block":true}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.request)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.want {
				t.Fatalf("request = %s, want %s", data, tc.want)
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
