package rod_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
)

type JSONUppercase string

func (s *JSONUppercase) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = JSONUppercase(strings.ToUpper(value))
	return nil
}

func TestPageEvalJSONValues(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank())

	var scalar int
	g.E(p.EvalJSON(&scalar, ` (a, b) => a + b; `, 20, 22))
	g.Eq(scalar, 42)
	p.MustEval(`() => window.value = 9`)
	g.E(p.EvalJSON(&scalar, `() => value`))
	g.Eq(scalar, 9)
	var boolean bool
	g.E(p.EvalJSON(&boolean, `function() { return this === window }`))
	g.True(boolean)
	var text string
	g.E(p.EvalJSON(&text, `value => value`, "quotes: '\"; newlines:\n"))
	g.Eq(text, "quotes: '\"; newlines:\n")
	var numbers []int
	g.E(p.EvalJSON(&numbers, `() => [1, 2, 3]`))
	g.Eq(numbers, []int{1, 2, 3})
	var object map[string]int
	g.E(p.EvalJSON(&object, `() => ({count: 3})`))
	g.Eq(object, map[string]int{"count": 3})
	var tagged struct {
		Count int `json:"answer"`
	}
	g.E(p.EvalJSON(&tagged, `() => Promise.resolve({answer: 42})`))
	g.Eq(tagged.Count, 42)
	pointer := new(3)
	g.E(p.EvalJSON(&pointer, `() => null`))
	g.Nil(pointer)
	var custom JSONUppercase
	g.E(p.EvalJSON(&custom, `() => "hello"`))
	g.Eq(custom, JSONUppercase("HELLO"))
	g.E(p.EvalJSON(&text, `() => new Date(0)`))
	g.Eq(text, "1970-01-01T00:00:00.000Z")
	g.E(p.EvalJSON(&scalar, `() => ({toJSON() { return 7 }, ignored: undefined})`))
	g.Eq(scalar, 7)

	remote := p.MustEvaluate(rod.Eval(`() => ({count: 8})`).ByObject())
	defer p.MustRelease(remote)
	g.E(p.EvalJSON(&scalar, `value => value.count`, remote))
	g.Eq(scalar, 8)
}

func TestPageEvalJSONSerializationErrors(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank())

	cases := []string{
		`() => undefined`,
		`() => function() {}`,
		`() => Symbol("value")`,
		`() => 1n`,
		`() => NaN`,
		`() => Infinity`,
		`() => -Infinity`,
		`() => ({value: undefined})`,
		`() => ({value() {}})`,
		`() => ({value: Symbol("value")})`,
		`() => ({value: 1n})`,
		`() => ({value: NaN})`,
		`() => [undefined]`,
		`() => [() => {}]`,
		`() => [Symbol("value")]`,
		`() => [1n]`,
		`() => [Infinity]`,
		`() => [, 1]`,
		`() => { const value = {}; value.self = value; return value }`,
		`() => ({get value() { throw new Error("getter failed") }})`,
		`() => ({toJSON() { throw new Error("toJSON failed") }})`,
		`() => ({toJSON() { return undefined }})`,
		`() => ({get value() { throw {toString() { throw null }} }})`,
	}
	for _, js := range cases {
		var value any
		err := p.EvalJSON(&value, js)
		var serialization *rod.JSONSerializationError
		if !errors.As(err, &serialization) {
			t.Fatalf("%s: expected serialization error, got %v", js, err)
		}
		if serialization.Message == "" {
			t.Fatalf("%s: missing serialization context", js)
		}
	}
	var value any
	err := p.EvalJSON(&value, `() => ({get value() { throw new Error("getter failed") }})`)
	g.Has(err.Error(), "getter failed")
}

func TestPageEvalJSONProjection(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><p id="item">hello</p>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	g.E(p.WaitLoad())

	var hidden map[string]int
	g.E(p.EvalJSON(&hidden, `() => {
		const value = {visible: 1, [Symbol("hidden")]: undefined};
		Object.defineProperty(value, "hidden", {value: undefined});
		return value;
	}`))
	g.Eq(hidden, map[string]int{"visible": 1})
	var projected struct {
		Map   map[string]int `json:"map"`
		Set   []string       `json:"set"`
		Text  string         `json:"text"`
		Error string         `json:"error"`
	}
	g.E(p.EvalJSON(&projected, `() => ({
		map: Object.fromEntries(new Map([["count", 2]])),
		set: Array.from(new Set(["one", "two"])),
		text: document.querySelector("#item").textContent,
		error: new Error("message").message
	})`))
	g.Eq(projected.Map, map[string]int{"count": 2})
	g.Eq(projected.Set, []string{"one", "two"})
	g.Eq(projected.Text, "hello")
	g.Eq(projected.Error, "message")
}

func TestPageEvalJSONDestinations(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank())
	p.MustEval(`() => window.effects = 0`)

	var pointer *int
	var mapping map[string]int
	for _, destination := range []any{pointer, 42, mapping, []int{1}, func() {}} {
		err := p.EvalJSON(destination, `() => ++window.effects`)
		var invalid *json.InvalidUnmarshalError
		if !errors.As(err, &invalid) {
			t.Fatalf("destination %T: expected InvalidUnmarshalError, got %v", destination, err)
		}
	}
	g.Eq(p.MustEval(`() => window.effects`).Int(), 0)
	g.E(p.EvalJSON(nil, `() => new Promise(resolve => setTimeout(() => {
		window.effects++;
		const cycle = {}; cycle.self = cycle; resolve(cycle);
	}, 10))`))
	g.Eq(p.MustEval(`() => window.effects`).Int(), 1)
	g.E(p.EvalJSON(nil, `() => Symbol("discarded")`))
	g.E(p.EvalJSON(nil, `() => ({get value() { throw new Error("not serialized") }})`))

	var number uint8
	for _, js := range []string{`() => "1"`, `() => 256`} {
		err := p.EvalJSON(&number, js)
		var mismatch *json.UnmarshalTypeError
		if !errors.As(err, &mismatch) {
			t.Fatalf("%s: expected UnmarshalTypeError, got %v", js, err)
		}
	}
}

func TestPageEvalJSONEvaluationErrors(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank())
	var value any
	for _, js := range []string{
		`() => { throw new Error("evaluation failed") }`,
		`() => Promise.reject(new Error("evaluation failed"))`,
	} {
		for _, destination := range []any{nil, &value} {
			err := p.EvalJSON(destination, js)
			var evaluation *rod.EvalError
			if !errors.As(err, &evaluation) {
				t.Fatalf("%s: expected EvalError, got %v", js, err)
			}
			g.Has(err.Error(), "evaluation failed")
			if evaluation.Exception != nil && evaluation.Exception.ObjectID != "" {
				t.Fatalf("evaluation retained exception object %q", evaluation.Exception.ObjectID)
			}
		}
	}
	err := p.Timeout(50*time.Millisecond).EvalJSON(&value, `() => new Promise(() => {})`)
	g.Is(err, context.DeadlineExceeded)

	g.mc.stub(1, proto.RuntimeCallFunctionOn{}, func(_ StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(nil), cdp.ErrCtxNotFound
	})
	g.E(p.EvalJSON(&value, `() => 3`))
	g.Eq(value, float64(3))
	err = p.Timeout(5*time.Second).EvalJSON(&value, `url => new Promise(() => { location.href = url })`, g.blank())
	g.Is(err, cdp.ErrCtxDestroyed)
	p.MustWaitLoad()
	g.E(p.EvalJSON(&value, `() => 4`))
	g.Eq(value, float64(4))
}

func TestPageEvalJSONExceptionCleanupError(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank())
	g.mc.stubErr(1, proto.RuntimeReleaseObject{})
	err := p.EvalJSON(nil, `() => { throw new Error("evaluation failed") }`)
	var evaluation *rod.EvalError
	if !errors.As(err, &evaluation) {
		t.Fatalf("expected EvalError, got %v", err)
	}
	g.Has(err.Error(), "release JavaScript exception")
	// The failed release remains visible to the caller, including the handle
	// needed to retry cleanup while the page remains open.
	g.E(p.Release(evaluation.Exception))
}

func TestPageEvalJSONReleasesExceptionObjects(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.blank())
	defer g.mc.resetCall()
	var value any
	for _, js := range []string{
		`() => { throw new Error("evaluation failed") }`,
		`() => Promise.reject(new Error("evaluation failed"))`,
	} {
		for _, destination := range []any{nil, &value} {
			ids := make(map[string]proto.RuntimeRemoteObjectID)
			g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
				data, err := g.mc.principal.Call(ctx, session, method, params)
				if err == nil && method == "Runtime.callFunctionOn" {
					var response proto.RuntimeCallFunctionOnResult
					if err := json.Unmarshal(data, &response); err != nil {
						return nil, err
					}
					if response.Result != nil && response.Result.ObjectID != "" {
						ids["result"] = response.Result.ObjectID
					}
					if response.ExceptionDetails != nil && response.ExceptionDetails.Exception != nil && response.ExceptionDetails.Exception.ObjectID != "" {
						ids["exception"] = response.ExceptionDetails.Exception.ObjectID
					}
				}
				return data, err
			})
			err := p.EvalJSON(destination, js)
			g.mc.resetCall()
			var evaluation *rod.EvalError
			if !errors.As(err, &evaluation) {
				t.Fatalf("%s: expected EvalError, got %v", js, err)
			}
			if len(ids) == 0 {
				t.Fatalf("%s: browser returned no exception object handles to check", js)
			}
			for source, id := range ids {
				request := proto.RuntimeGetProperties{ObjectID: id, OwnProperties: new(true)}
				_, err := g.mc.principal.Call(p.GetContext(), string(p.SessionID), request.ProtoReq(), request)
				if !errors.Is(err, cdp.ErrObjNotFound) {
					t.Errorf("%s: %s object %q is still accessible after EvalJSON: %v", js, source, id, err)
				}
			}
		}
	}
}
