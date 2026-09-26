package main_test

import (
	"context"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/proto"
)

// replayClient answers every command with one captured protocol response.
type replayClient []byte

func (client replayClient) Call(context.Context, string, string, any) ([]byte, error) {
	return client, nil
}

// Large protocol results captured from Chrome, decoded through the generated
// command bindings. The replay subbenchmarks measure only Rod's decoding of the
// response; the chrome subbenchmarks include the browser round trip.
func BenchmarkProtocolDecode(b *testing.B) {
	page := benchmarkWaitPage(b)
	page.MustEval(`rows => {
		for (let i = 0; i < rows; i++) {
			const row = document.createElement('div');
			row.className = 'row';
			row.dataset.index = i;
			row.innerHTML = '<span>text ' + i + '</span><a href="#' + i + '">link</a><input value="' + i + '">';
			document.body.append(row);
		}
	}`, 4000)
	document, err := page.Call(page.GetContext(), string(page.SessionID), "DOM.getDocument", proto.DOMGetDocument{Depth: new(-1)})
	if err != nil {
		b.Fatal(err)
	}
	array, err := page.Evaluate(rod.Eval(`() => Array.from({length: 10000}, (_, i) => ({index: i, name: 'item ' + i}))`).ByObject())
	if err != nil {
		b.Fatal(err)
	}
	properties := proto.RuntimeGetProperties{ObjectID: array.ObjectID, OwnProperties: new(true), GeneratePreview: new(true)}
	propertyData, err := page.Call(page.GetContext(), string(page.SessionID), properties.ProtoReq(), properties)
	if err != nil {
		b.Fatal(err)
	}
	call := proto.RuntimeCallFunctionOn{ObjectID: array.ObjectID, FunctionDeclaration: `function() { return this.length }`, ReturnByValue: new(true)}
	callData, err := page.Call(page.GetContext(), string(page.SessionID), call.ProtoReq(), call)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("replay/DOM.getDocument", func(b *testing.B) {
		b.SetBytes(int64(len(document)))
		b.ReportAllocs()
		for b.Loop() {
			if _, err := (proto.DOMGetDocument{Depth: new(-1)}).Call(replayClient(document)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("replay/Runtime.getProperties", func(b *testing.B) {
		b.SetBytes(int64(len(propertyData)))
		b.ReportAllocs()
		for b.Loop() {
			if _, err := properties.Call(replayClient(propertyData)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("replay/Runtime.callFunctionOn", func(b *testing.B) {
		b.SetBytes(int64(len(callData)))
		b.ReportAllocs()
		for b.Loop() {
			if _, err := call.Call(replayClient(callData)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("chrome/DOM.getDocument", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := (proto.DOMGetDocument{Depth: new(-1)}).Call(page); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("chrome/Runtime.getProperties", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := properties.Call(page); err != nil {
				b.Fatal(err)
			}
		}
	})
}
