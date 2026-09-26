package proto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rah-0/rod/lib/jsonvalue"
)

var (
	jsonValueType = reflect.TypeFor[jsonvalue.Value]()
	bytesType     = reflect.TypeFor[[]byte]()
)

// member is a struct field as the decoding rules see it, derived only from
// the Go type, JSON tag and documentation of a generated struct.
type member struct {
	name     string
	index    int
	required bool
	typ      reflect.Type
}

// object returns the struct type of a *T field, or nil.
func (m member) object() reflect.Type {
	if m.typ.Kind() == reflect.Pointer && m.typ.Elem().Kind() == reflect.Struct {
		return m.typ.Elem()
	}
	return nil
}

// list reports whether the field is a JSON array.
func (m member) list() bool {
	return m.typ.Kind() == reflect.Slice && m.typ.Elem().Kind() != reflect.Uint8
}

// nullable reports whether a required field accepts null: jsonvalue.Value
// holds null as a value, and Chrome sends null for numbers that JSON cannot
// represent.
func (m member) nullable() bool {
	return m.typ == jsonValueType || m.typ.Kind() == reflect.Float64
}

// notRequired returns, for each generated struct, the fields whose
// documentation marks them experimental, deprecated, optional, or optional in
// older browsers. The rules do not require them. Optional fields of type
// jsonvalue.Value have no omitempty option.
func notRequired(t *testing.T) map[string]map[string]bool {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if st, ok := spec.Type.(*ast.StructType); ok {
				for _, field := range st.Fields.List {
					if len(field.Names) != 1 || field.Doc == nil {
						continue
					}
					doc, name := field.Doc.Text(), field.Names[0].Name
					versioned := strings.Contains(doc, "\nDeprecated: This protocol API is deprecated.")
					markers := strings.TrimPrefix(doc, name+" ")
					for _, marker := range []string{"(experimental) ", "(optional) ", "(optional in older browsers) "} {
						if rest, ok := strings.CutPrefix(markers, marker); ok {
							markers, versioned = rest, true
						}
					}
					if versioned {
						if fields[spec.Name.Name] == nil {
							fields[spec.Name.Name] = map[string]bool{}
						}
						fields[spec.Name.Name][name] = true
					}
				}
			}
			return false
		})
	}
	for typ, field := range map[string]string{
		"CSSGetComputedStyleForNodeResult": "ExtraFields",    // experimental
		"PageGetLayoutMetricsResult":       "LayoutViewport", // deprecated
		"DebuggerScriptParsed":             "BuildID",        // not sent by Chromium 128
	} {
		if !fields[typ][field] {
			t.Fatalf("%s.%s is not documented as versioned", typ, field)
		}
	}
	return fields
}

// decodingOracle derives the expected decoding rules from the generated Go
// types and their documentation, independently of the generator.
type decodingOracle struct {
	members map[reflect.Type][]member
	// witness is a member that makes a type reject some input. It refers only
	// to types decided earlier, so broken values are always finite.
	witness map[reflect.Type]member
}

func newDecodingOracle(t *testing.T) *decodingOracle {
	o := &decodingOracle{members: map[reflect.Type][]member{}, witness: map[reflect.Type]member{}}
	versioned := notRequired(t)
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ == jsonValueType {
			return
		}
		if _, seen := o.members[typ]; seen {
			return
		}
		var members []member
		for i := range typ.NumField() {
			field := typ.Field(i)
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			optional := strings.Contains(options, "omitempty") || strings.Contains(options, "omitzero") || versioned[typ.Name()][field.Name]
			members = append(members, member{name, i, !optional, field.Type})
		}
		o.members[typ] = members
		for _, m := range members {
			visit(m.typ)
		}
	}
	// Command results and events are decoded from browser data; command
	// parameters and the types used only by them are not.
	request := reflect.TypeFor[Request]()
	for name, typ := range types {
		_, method, _ := strings.Cut(name, ".")
		if method[0] >= 'a' && method[0] <= 'z' && !typ.Implements(request) {
			visit(typ)
		}
	}
	if len(o.members) < len(types)/3 {
		t.Fatalf("only %d decoded structs found", len(o.members))
	}
	for changed := true; changed; {
		changed = false
		for typ, members := range o.members {
			if _, ok := o.witness[typ]; ok {
				continue
			}
			for _, m := range members {
				_, elemRejects := o.witness[m.object()]
				if m.required || m.list() && m.typ.Elem() != jsonValueType || m.object() != nil && elemRejects {
					o.witness[typ] = m
					changed = true
					break
				}
			}
		}
	}
	return o
}

// zero returns the smallest JSON value of typ.
func (o *decodingOracle) zero(t *testing.T, typ reflect.Type, depth int) any {
	switch {
	case typ == jsonValueType:
		return nil
	case typ == bytesType:
		return ""
	case typ.Kind() == reflect.Pointer:
		return o.minimal(t, typ.Elem(), depth+1)
	case typ.Kind() == reflect.Slice:
		return []any{}
	case typ.Kind() == reflect.Map:
		return map[string]any{}
	case typ.Kind() == reflect.String:
		return ""
	case typ.Kind() == reflect.Bool:
		return false
	case typ.Kind() == reflect.Int, typ.Kind() == reflect.Float64:
		return 0
	}
	t.Fatalf("unexpected field type %s", typ)
	return nil
}

// minimal returns the smallest valid JSON object of typ.
func (o *decodingOracle) minimal(t *testing.T, typ reflect.Type, depth int) map[string]any {
	if depth > 32 {
		t.Fatalf("required members of %s do not terminate", typ)
	}
	value := map[string]any{}
	for _, m := range o.members[typ] {
		if m.required {
			value[m.name] = o.zero(t, m.typ, depth)
		}
	}
	return value
}

// broken returns a JSON object of typ that fails to decode, and the path of
// the failure.
func (o *decodingOracle) broken(t *testing.T, typ reflect.Type) (map[string]any, string) {
	m := o.witness[typ]
	value := o.minimal(t, typ, 0)
	switch {
	case m.required:
		delete(value, m.name)
		return value, m.name
	case m.list():
		value[m.name] = []any{nil}
		return value, m.name + "[0]"
	default:
		inner, path := o.broken(t, m.object())
		value[m.name] = inner
		return value, m.name + "." + path
	}
}

// TestDecodingRules checks every decoded protocol struct against the rules
// documented on Decoding.Unmarshal: which types are decoded by generated
// code, and which JSON inputs they accept or reject with which path.
func TestDecodingRules(t *testing.T) {
	o := newDecodingOracle(t)
	for typ := range types {
		typ := types[typ]
		_, decoded := o.members[typ]
		if got := reflect.PointerTo(typ).Implements(reflect.TypeFor[decodable]()); got != decoded {
			t.Errorf("%s has a decoder: %t, want %t", typ.Name(), got, decoded)
		}
	}
	if t.Failed() {
		return
	}
	required := 0
	for typ, members := range o.members {
		expectDecode(t, typ, o.minimal(t, typ, 0), "")
		for _, m := range members {
			with := func(v any) map[string]any {
				value := o.minimal(t, typ, 0)
				value[m.name] = v
				return value
			}
			if m.required {
				required++
				missing := o.minimal(t, typ, 0)
				delete(missing, m.name)
				expectDecode(t, typ, missing, m.name)
				if m.nullable() {
					expectDecode(t, typ, with(nil), "")
				} else {
					expectDecode(t, typ, with(nil), m.name)
				}
			} else {
				expectDecode(t, typ, with(nil), "")
			}
			if m.list() {
				entry := o.zero(t, m.typ.Elem(), 0)
				expectDecode(t, typ, with([]any{entry}), "")
				if m.typ.Elem() == jsonValueType || m.typ.Elem().Kind() == reflect.Float64 {
					expectDecode(t, typ, with([]any{entry, nil}), "")
				} else {
					expectDecode(t, typ, with([]any{entry, nil}), m.name+"[1]")
				}
				if elem := m.typ.Elem(); elem.Kind() == reflect.Pointer {
					if _, rejects := o.witness[elem.Elem()]; rejects {
						inner, path := o.broken(t, elem.Elem())
						expectDecode(t, typ, with([]any{entry, inner}), m.name+"[1]."+path)
					}
				}
			}
			if object := m.object(); object != nil {
				if _, rejects := o.witness[object]; rejects {
					inner, path := o.broken(t, object)
					expectDecode(t, typ, with(inner), m.name+"."+path)
				}
			}
		}
	}
	t.Logf("%d decoded structs with %d required members", len(o.members), required)
}

// expectDecode decodes value as typ and expects the failure path, or success
// when path is empty. Lenient decoding accepts every input.
func expectDecode(t *testing.T, typ reflect.Type, value map[string]any, path string) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := DecodeLenient.Unmarshal(data, reflect.New(typ).Interface()); err != nil {
		t.Errorf("%s lenient decoding of %s: %v", typ.Name(), data, err)
	}
	err = Unmarshal(data, reflect.New(typ).Interface())
	if path == "" {
		if err != nil {
			t.Errorf("%s rejected %s: %v", typ.Name(), data, err)
		}
		return
	}
	missing, ok := errors.AsType[*MissingFieldError](err)
	if !ok || !errors.Is(err, ErrMissingField) || missing.Type != typ.Name() || missing.Path != path {
		t.Errorf("%s decoding %s = %v, want path %s", typ.Name(), data, err, path)
	}
}

// sample returns a JSON value of typ that sets every field, with nested
// objects down to depth levels and required objects below that.
func (o *decodingOracle) sample(t *testing.T, typ reflect.Type, depth int) any {
	switch {
	case typ == jsonValueType:
		return map[string]any{"list": []any{1.5, "text", nil, true}}
	case typ == bytesType:
		return base64.StdEncoding.EncodeToString([]byte("bytes\x00\xff"))
	case typ.Kind() == reflect.Pointer && typ.Elem().Kind() == reflect.Struct:
		if depth <= 0 {
			return o.minimal(t, typ.Elem(), 0)
		}
		value := map[string]any{}
		for _, m := range o.members[typ.Elem()] {
			if m.required || depth > 0 {
				value[m.name] = o.sample(t, m.typ, depth-1)
			}
		}
		return value
	case typ.Kind() == reflect.Pointer:
		return o.sample(t, typ.Elem(), depth)
	case typ.Kind() == reflect.Slice:
		return []any{o.sample(t, typ.Elem(), depth), o.sample(t, typ.Elem(), depth)}
	case typ.Kind() == reflect.Map:
		return map[string]any{"key": "value", "number": 2}
	case typ.Kind() == reflect.String:
		return "text é \"quoted\" \\ / \n   \U0001F600"
	case typ.Kind() == reflect.Bool:
		return true
	case typ.Kind() == reflect.Int:
		return -42
	case typ.Kind() == reflect.Float64:
		return 1.25e-3
	}
	t.Fatalf("unexpected field type %s", typ)
	return nil
}

// TestDecodingMatchesEncodingJSON decodes a value that sets every field of
// each decoded struct and compares the result with encoding/json.
func TestDecodingMatchesEncodingJSON(t *testing.T) {
	o := newDecodingOracle(t)
	for typ := range o.members {
		data, err := json.Marshal(o.sample(t, reflect.PointerTo(typ), 2))
		if err != nil {
			t.Fatal(err)
		}
		want := reflect.New(typ).Interface()
		if err := json.Unmarshal(data, want); err != nil {
			t.Fatalf("%s: %v", typ.Name(), err)
		}
		for _, mode := range []Decoding{DecodeStrict, DecodeLenient} {
			got := reflect.New(typ).Interface()
			if err := mode.Unmarshal(data, got); err != nil {
				t.Errorf("%s: %v", typ.Name(), err)
			} else if !reflect.DeepEqual(got, want) {
				t.Errorf("%s decoded %s as %+v, want %+v", typ.Name(), data, got, want)
			}
		}
	}
}

func TestUnmarshalMissingField(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		data  string
		err   string
	}{
		{"missing object", &RuntimeEvaluateResult{}, `{}`, "proto: RuntimeEvaluateResult: result is missing or null"},
		{"null object", &RuntimeEvaluateResult{}, `{"result":null}`, "proto: RuntimeEvaluateResult: result is missing or null"},
		{"null as the whole response", &RuntimeCallFunctionOnResult{}, `null`, "proto: RuntimeCallFunctionOnResult: result is missing or null"},
		{"missing string", &RuntimeEvaluateResult{}, `{"result":{}}`, "proto: RuntimeEvaluateResult: result.type is missing or null"},
		{"null string", &RuntimeEvaluateResult{}, `{"result":{"type":null}}`, "proto: RuntimeEvaluateResult: result.type is missing or null"},
		{"missing integer", &DOMDescribeNodeResult{}, `{"node":{"backendNodeId":1,"nodeType":1,"nodeName":"A","localName":"a","nodeValue":""}}`,
			"proto: DOMDescribeNodeResult: node.nodeId is missing or null"},
		{"missing boolean", &NetworkGetResponseBodyResult{}, `{"body":""}`, "proto: NetworkGetResponseBodyResult: base64Encoded is missing or null"},
		{"missing array", &RuntimeGetPropertiesResult{}, `{}`, "proto: RuntimeGetPropertiesResult: result is missing or null"},
		{"null array", &RuntimeGetPropertiesResult{}, `{"result":null}`, "proto: RuntimeGetPropertiesResult: result is missing or null"},
		{"missing map", &NetworkResponseReceivedExtraInfo{}, `{"requestId":"r","blockedCookies":[],"resourceIPAddressSpace":"Public","statusCode":200}`,
			"proto: NetworkResponseReceivedExtraInfo: headers is missing or null"},
		{"nested null entry", &DOMGetDocumentResult{}, `{"root":{"nodeId":1,"backendNodeId":1,"nodeType":9,"nodeName":"#document","localName":"","nodeValue":"","children":[null]}}`,
			"proto: DOMGetDocumentResult: root.children[0] is missing or null"},
		{"null string entry", &DOMGetAttributesResult{}, `{"attributes":["id",null]}`, "proto: DOMGetAttributesResult: attributes[1] is missing or null"},
		{"null target", &TargetGetTargetsResult{}, `{"targetInfos":[null]}`, "proto: TargetGetTargetsResult: targetInfos[0] is missing or null"},
		{"event", &TargetTargetCreated{}, `{}`, "proto: TargetTargetCreated: targetInfo is missing or null"},
		{"exact names", &TargetTargetDestroyed{}, `{"TargetId":"t"}`, "proto: TargetTargetDestroyed: targetId is missing or null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Unmarshal([]byte(test.data), test.value)
			if test.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			missing, ok := errors.AsType[*MissingFieldError](err)
			if !ok || !errors.Is(err, ErrMissingField) || err.Error() != test.err {
				t.Fatalf("Unmarshal = %v", err)
			}
			if missing.Type != reflect.TypeOf(test.value).Elem().Name() {
				t.Fatalf("type = %q", missing.Type)
			}
			if err := DecodeLenient.Unmarshal([]byte(test.data), reflect.New(reflect.TypeOf(test.value).Elem()).Interface()); err != nil {
				t.Fatalf("lenient decoding: %v", err)
			}
		})
	}

	// Null is a value of jsonvalue.Value and is accepted for numbers, which
	// Chrome sends as null when JSON cannot represent them.
	var cookies NetworkGetCookiesResult
	if err := Unmarshal([]byte(`{"cookies":[{"name":"n","value":"v","domain":"d","path":"/","expires":null,"size":2,"httpOnly":false,"secure":false,"session":true,"priority":"Medium","sourceScheme":"Secure","sourcePort":443}]}`), &cookies); err != nil {
		t.Fatal(err)
	}
	if cookies.Cookies[0].Expires != 0 || cookies.Cookies[0].Name != "n" {
		t.Fatalf("cookie = %+v", cookies.Cookies[0])
	}
}

// Browsers older or newer than the generated schema can omit required members
// that the schema marks as experimental or deprecated, or that
// schema-compatibility.json records, so those are not required.
func TestUnmarshalVersionDependentMembers(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		data  string
		path  string
	}{
		// Chromium 128 does not send extraFields, which Chrome 152 requires.
		{"experimental field from a newer schema", &CSSGetComputedStyleForNodeResult{}, `{"computedStyle":[{"name":"display","value":"block"}]}`, ""},
		{"deprecated fields", &PageGetLayoutMetricsResult{}, `{"cssLayoutViewport":{"pageX":0,"pageY":0,"clientWidth":1,"clientHeight":1},"cssVisualViewport":{"offsetX":0,"offsetY":0,"pageX":0,"pageY":0,"clientWidth":1,"clientHeight":1,"scale":1},"cssContentSize":{"x":0,"y":0,"width":1,"height":1}}`, ""},
		{"stable field next to deprecated fields", &PageGetLayoutMetricsResult{}, `{"cssLayoutViewport":{"pageX":0,"pageY":0,"clientWidth":1,"clientHeight":1},"cssVisualViewport":{"offsetX":0,"offsetY":0,"pageX":0,"pageY":0,"clientWidth":1,"clientHeight":1,"scale":1}}`, "cssContentSize"},
		{"experimental field without value", &PageGetAppManifestResult{}, `{"url":"u","errors":[],"manifest":null}`, ""},
		{"experimental field checked when present", &PageGetAppManifestResult{}, `{"url":"u","errors":[],"manifest":{"icons":[null]}}`, "manifest.icons[0]"},
		{"deprecated event field", &SecuritySecurityStateChanged{}, `{"securityState":"secure"}`, ""},
		// Chromium 128 does not send buildId or base64Encoded.
		{"member that older browsers omit", &DebuggerScriptParsed{}, `{"scriptId":"1","url":"","startLine":0,"startColumn":0,"endLine":0,"endColumn":0,"executionContextId":1,"hash":""}`, ""},
		{"result member that older browsers omit", &NetworkGetRequestPostDataResult{}, `{"postData":"a=1"}`, ""},
		{"stable member next to one older browsers omit", &DebuggerScriptParsed{}, `{"scriptId":"1","url":"","startLine":0,"startColumn":0,"endLine":0,"endColumn":0,"executionContextId":1}`, "hash"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Unmarshal([]byte(test.data), test.value)
			if test.path == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if missing, ok := errors.AsType[*MissingFieldError](err); !ok || missing.Path != test.path {
				t.Fatalf("Unmarshal = %v, want path %s", err, test.path)
			}
		})
	}
}

// TestUnmarshalSemantics covers the encoding/json behavior that decoding
// keeps, and its documented differences.
func TestUnmarshalSemantics(t *testing.T) {
	const node = `"nodeId":1,"backendNodeId":2,"nodeType":1,"nodeName":"DIV","localName":"div","nodeValue":""`
	t.Run("unknown and duplicate members", func(t *testing.T) {
		var result DOMDescribeNodeResult
		data := `{"future":{"a":[1,{"b":null}]},"node":{` + node + `,"nodeName":"SPAN","children":[{` + node + `}],"children":[],"extra":"x"},"node2":null}`
		if err := Unmarshal([]byte(data), &result); err != nil {
			t.Fatal(err)
		}
		if result.Node.NodeName != "SPAN" || result.Node.Children == nil || len(result.Node.Children) != 0 {
			t.Fatalf("node = %+v", result.Node)
		}
	})
	t.Run("duplicate object members are merged", func(t *testing.T) {
		complete, partial := `"node":{`+node+`}`, `"node":{"nodeId":5}`
		for _, data := range []string{`{` + complete + `,` + partial + `}`, `{` + partial + `,` + complete + `}`} {
			var want, lenient DOMDescribeNodeResult
			if err := json.Unmarshal([]byte(data), &want); err != nil {
				t.Fatal(err)
			}
			if err := DecodeLenient.Unmarshal([]byte(data), &lenient); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(lenient, want) || want.Node.BackendNodeID != 2 || want.Node.NodeName != "DIV" {
				t.Fatalf("lenient node = %+v, want the merged %+v", lenient.Node, want.Node)
			}
			// Strict decoding checks each occurrence, not the merged object.
			var strict DOMDescribeNodeResult
			if err := Unmarshal([]byte(data), &strict); !errors.Is(err, ErrMissingField) {
				t.Fatalf("strict decoding of %s = %v, want ErrMissingField", data, err)
			}
		}
	})
	t.Run("member names match exactly", func(t *testing.T) {
		var event PageFrameStoppedLoading
		if err := DecodeLenient.Unmarshal([]byte(`{"FrameId":"upper","frameid":"lower"}`), &event); err != nil || event.FrameID != "" {
			t.Fatalf("event = %+v, %v", event, err)
		}
		// Escaped names are compared after unescaping.
		if err := Unmarshal([]byte(`{"frameId":"escaped"}`), &event); err != nil || event.FrameID != "escaped" {
			t.Fatalf("event = %+v, %v", event, err)
		}
	})
	t.Run("null for optional fields", func(t *testing.T) {
		result := RuntimeEvaluateResult{ExceptionDetails: &RuntimeExceptionDetails{}}
		result.Result = &RuntimeRemoteObject{Type: "number", Subtype: "kept", Preview: &RuntimeObjectPreview{}}
		data := `{"result":{"type":"object","subtype":null,"preview":null,"value":null},"exceptionDetails":null}`
		if err := Unmarshal([]byte(data), &result); err != nil {
			t.Fatal(err)
		}
		if result.Result.Type != "object" || result.Result.Subtype != "kept" || result.Result.Preview != nil || result.ExceptionDetails != nil {
			t.Fatalf("result = %+v %+v", result, result.Result)
		}
		if result.Result.Value.IsZero() || result.Result.Value.Val() != nil {
			t.Fatalf("explicit null value = %#v", result.Result.Value.Raw())
		}
	})
	t.Run("strings", func(t *testing.T) {
		for _, data := range []string{
			`{"frameId":"plain ascii"}`,
			`{"frameId":"escapes \" \\ \/ \b \f \n \r \t é 😀"}`,
			`{"frameId":"invalid utf-8 ` + "\xff\xfe" + ` and lone surrogate \ud800"}`,
			`{"frameId":"non-ASCII é 😀"}`,
		} {
			var got, want PageFrameStoppedLoading
			if err := json.Unmarshal([]byte(data), &want); err != nil {
				t.Fatal(err)
			}
			if err := Unmarshal([]byte(data), &got); err != nil || got != want {
				t.Fatalf("decoded %q as %q, %v; want %q", data, got.FrameID, err, want.FrameID)
			}
		}
	})
	t.Run("custom types", func(t *testing.T) {
		var event NetworkLoadingFinished
		if err := Unmarshal([]byte(`{"requestId":"r","timestamp":123.5,"encodedDataLength":10}`), &event); err != nil {
			t.Fatal(err)
		}
		if event.Timestamp.Duration().Milliseconds() != 123500 {
			t.Fatalf("timestamp = %v", event.Timestamp)
		}
		var screenshot PageCaptureScreenshotResult
		if err := Unmarshal([]byte(`{"data":"AQL\/"}`), &screenshot); err != nil || string(screenshot.Data) != "\x01\x02\xff" {
			t.Fatalf("data = %q, %v", screenshot.Data, err)
		}
		headers := NetworkHeaders{"kept": jsonvalue.New("old")}
		response := NetworkResponseReceivedExtraInfo{Headers: headers}
		if err := Unmarshal([]byte(`{"requestId":"r","blockedCookies":[],"headers":{"a":"1","b":[2]},"resourceIPAddressSpace":"Public","statusCode":200}`), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Headers) != 3 || response.Headers["a"].Val() != "1" || response.Headers["kept"].Val() != "old" {
			t.Fatalf("headers = %v", response.Headers)
		}
	})
	t.Run("map keys", func(t *testing.T) {
		// Keys are decoded as strings are: escapes are unquoted, and invalid
		// UTF-8 and lone surrogates are replaced.
		data := `{"requestId":"r","blockedCookies":[],"resourceIPAddressSpace":"Public","statusCode":200,"headers":{` +
			`"plain":"1","escaped \u00e9":"2","invalid ` + "\xff\xfe" + `":"3","lone \ud800":"4"}}`
		var got, want NetworkResponseReceivedExtraInfo
		if err := json.Unmarshal([]byte(data), &want); err != nil {
			t.Fatal(err)
		}
		if err := Unmarshal([]byte(data), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Headers) != len(want.Headers) {
			t.Fatalf("headers = %q, want %q", slices.Sorted(maps.Keys(got.Headers)), slices.Sorted(maps.Keys(want.Headers)))
		}
		for key, value := range want.Headers {
			if got.Headers[key].Val() != value.Val() {
				t.Fatalf("headers = %q, want %q", slices.Sorted(maps.Keys(got.Headers)), slices.Sorted(maps.Keys(want.Headers)))
			}
		}
	})
	t.Run("type errors", func(t *testing.T) {
		var result DOMDescribeNodeResult
		err := Unmarshal([]byte(`{"node":{`+node+`,"children":[{`+node+`,"nodeType":"1"}]}}`), &result)
		typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err)
		if !ok || typeErr.Struct != "DOMDescribeNodeResult" || typeErr.Field != "node.children[0].nodeType" || typeErr.Value != "string" || typeErr.Type != reflect.TypeFor[int]() {
			t.Fatalf("Unmarshal = %#v", err)
		}
		for _, data := range []string{`{"node":{` + node + `,"nodeId":1.5}}`, `{"node":{` + node + `,"nodeId":1e3}}`, `{"node":[]}`, `[]`} {
			if _, ok := errors.AsType[*json.UnmarshalTypeError](Unmarshal([]byte(data), &result)); !ok {
				t.Errorf("Unmarshal(%s) did not report a type error", data)
			}
		}
		var screenshot PageCaptureScreenshotResult
		err = Unmarshal([]byte(`{"data":"not base64"}`), &screenshot)
		if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok || typeErr.Field != "data" || !errors.As(err, new(base64.CorruptInputError)) {
			t.Fatalf("Unmarshal = %v", err)
		}
	})
	t.Run("type errors match encoding/json", func(t *testing.T) {
		// The errors equal those of encoding/json, which writes an array index
		// in Field as a member, such as "children.0" for "children[0]".
		index := regexp.MustCompile(`\[(\d+)\]`)
		for _, test := range []struct {
			value  func() any
			format string
		}{
			{func() any { return new(DOMDescribeNodeResult) }, `%s`},
			{func() any { return new(DOMDescribeNodeResult) }, `{"node":%s}`},
			{func() any { return new(DOMDescribeNodeResult) }, `{"node":{` + node + `,"nodeId":%s}}`},
			{func() any { return new(DOMDescribeNodeResult) }, `{"node":{` + node + `,"nodeName":%s}}`},
			{func() any { return new(DOMDescribeNodeResult) }, `{"node":{` + node + `,"children":%s}}`},
			{func() any { return new(DOMDescribeNodeResult) }, `{"node":{` + node + `,"children":[{` + node + `,"nodeType":%s}]}}`},
			{func() any { return new(DOMGetAttributesResult) }, `{"attributes":["a",%s]}`},
			{func() any { return new(DOMGetBoxModelResult) }, `{"model":{"content":%s,"padding":[],"border":[],"margin":[],"width":1,"height":1}}`},
			{func() any { return new(DOMGetContentQuadsResult) }, `{"quads":[%s]}`},
			{func() any { return new(NetworkLoadingFinished) }, `{"requestId":"r","timestamp":%s,"encodedDataLength":1}`},
			{func() any { return new(NetworkResponseReceivedExtraInfo) }, `{"requestId":"r","blockedCookies":[],"resourceIPAddressSpace":"Public","statusCode":200,"headers":%s}`},
			{func() any { return new(PageCaptureScreenshotResult) }, `{"data":%s}`},
			{func() any { return new(RuntimeEvaluateResult) }, `{"result":{"type":"number"},"exceptionDetails":%s}`},
			{func() any { return new(TargetGetTargetInfoResult) }, `{"targetInfo":{"targetId":"t","type":"page","title":"","url":"","attached":%s}}`},
		} {
			for _, value := range []string{`1.5`, `1e3`, `"not base64"`, `true`, `[1]`, `{"a":1}`} {
				if _, binary := test.value().(*PageCaptureScreenshotResult); binary && value == `[1]` {
					continue // binary fields accept only base64 strings
				}
				data := fmt.Sprintf(test.format, value)
				want, wantOK := errors.AsType[*json.UnmarshalTypeError](json.Unmarshal([]byte(data), test.value()))
				got, gotOK := errors.AsType[*json.UnmarshalTypeError](Unmarshal([]byte(data), test.value()))
				if wantOK != gotOK || wantOK && (got.Value != want.Value || got.Type != want.Type || got.Offset != want.Offset ||
					got.Struct != want.Struct || index.ReplaceAllString(got.Field, ".$1") != want.Field || fmt.Sprint(got.Err) != fmt.Sprint(want.Err)) {
					t.Errorf("Unmarshal(%s) = %#v, want %#v", data, got, want)
				}
			}
		}
	})
	t.Run("syntax errors", func(t *testing.T) {
		for _, data := range []string{`{`, `{"frameId":"f"} {}`, `{"frameId":"f"} x`, ``, `{"frameId":}`,
			// Invalid JSON after a type error or a missing field is still a syntax error.
			`0000`, `{"frameId":1} x`, `{} x`} {
			var event PageFrameStoppedLoading
			if _, ok := errors.AsType[*json.SyntaxError](Unmarshal([]byte(data), &event)); !ok {
				t.Errorf("Unmarshal(%q) did not report a syntax error", data)
			}
		}
	})
	t.Run("other types use encoding/json", func(t *testing.T) {
		var loose map[string]any
		if err := Unmarshal([]byte(`{"result":null}`), &loose); err != nil {
			t.Fatal(err)
		}
		var request TargetCreateTarget
		if err := Unmarshal([]byte(`{"URL":"about:blank"}`), &request); err != nil || request.URL != "about:blank" {
			t.Fatalf("request = %+v, %v", request, err)
		}
		if _, ok := errors.AsType[*json.InvalidUnmarshalError](Unmarshal([]byte(`{}`), (*RuntimeEvaluateResult)(nil))); !ok {
			t.Fatal("nil target accepted")
		}
	})
}

type responseClient string

func (c responseClient) Call(context.Context, string, string, any) ([]byte, error) {
	return []byte(c), nil
}

type decodingClient struct {
	responseClient
	mode Decoding
}

func (c decodingClient) GetDecoding() Decoding { return c.mode }

func TestCallDecoding(t *testing.T) {
	result, err := RuntimeEvaluate{Expression: "1"}.Call(responseClient(`{"exceptionDetails":{"exceptionId":1,"text":"","lineNumber":0,"columnNumber":0}}`))
	if missing, ok := errors.AsType[*MissingFieldError](err); !ok || missing.Type != "RuntimeEvaluateResult" || missing.Path != "result" {
		t.Fatalf("Call = %v", err)
	}
	if result == nil || result.ExceptionDetails == nil {
		t.Fatal("the decoded part of the result is lost")
	}
	strict := decodingClient{`{"targetInfo":{"targetId":"page"}}`, DecodeStrict}
	if _, err := (TargetGetTargetInfo{}).Call(strict); !errors.Is(err, ErrMissingField) {
		t.Fatalf("strict Call = %v", err)
	}
	lenient := decodingClient{strict.responseClient, DecodeLenient}
	info, err := (TargetGetTargetInfo{}).Call(lenient)
	if err != nil || info.TargetInfo.TargetID != "page" || info.TargetInfo.Type != "" {
		t.Fatalf("lenient Call = %+v, %v", info, err)
	}
	// Commands without results do not decode the response.
	if err := (PageEnable{}).Call(responseClient(`null`)); err != nil {
		t.Fatal(err)
	}
}

// Decoding cost of the generated decoders compared with encoding/json. The
// document is a tree of about 22,000 nodes shaped like a DOM.getDocument
// response, and the properties are 10,000 descriptors with previews.
func BenchmarkUnmarshal(b *testing.B) {
	var node func(depth int) map[string]any
	id := 0
	node = func(depth int) map[string]any {
		id++
		value := map[string]any{"nodeId": id, "backendNodeId": id, "nodeType": 1, "nodeName": "DIV", "localName": "div",
			"nodeValue": "", "attributes": []string{"class", "row"}}
		if depth > 0 {
			var children []any
			for range 4 {
				children = append(children, node(depth-1))
			}
			value["children"] = children
			value["childNodeCount"] = len(children)
		}
		return value
	}
	document, err := json.Marshal(map[string]any{"root": node(7)})
	if err != nil {
		b.Fatal(err)
	}
	var properties []any
	for i := range 10000 {
		properties = append(properties, map[string]any{"name": fmt.Sprint(i), "configurable": true, "enumerable": true, "writable": true, "isOwn": true,
			"value": map[string]any{"type": "object", "className": "Object", "description": "Object", "objectId": fmt.Sprint("1.2.", i),
				"preview": map[string]any{"type": "object", "description": "Object", "overflow": false, "properties": []any{
					map[string]any{"name": "index", "type": "number", "value": fmt.Sprint(i)},
					map[string]any{"name": "name", "type": "string", "value": "item"},
				}}}})
	}
	propertyData, err := json.Marshal(map[string]any{"result": properties})
	if err != nil {
		b.Fatal(err)
	}
	for _, input := range []struct {
		name string
		data []byte
		new  func() any
	}{
		{"DOMGetDocumentResult", document, func() any { return &DOMGetDocumentResult{} }},
		{"RuntimeGetPropertiesResult", propertyData, func() any { return &RuntimeGetPropertiesResult{} }},
		{"RuntimeCallFunctionOnResult", []byte(`{"result":{"type":"number","value":16,"description":"16"}}`), func() any { return &RuntimeCallFunctionOnResult{} }},
	} {
		for _, decoder := range []struct {
			name   string
			decode func([]byte, any) error
		}{{"json", json.Unmarshal}, {"proto", Unmarshal}, {"lenient", DecodeLenient.Unmarshal}} {
			b.Run(input.name+"/"+decoder.name, func(b *testing.B) {
				b.SetBytes(int64(len(input.data)))
				b.ReportAllocs()
				for b.Loop() {
					if err := decoder.decode(input.data, input.new()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
