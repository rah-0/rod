package rod_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/proto"
)

const queryContextHTML = `<!doctype html>
<section id="main"><span class="label">Main</span></section>
<iframe srcdoc='<section id="child"><span class="label">Child</span></section><section id="adopted"><span class="label">Adopted</span></section>'></iframe>`

type queryContextCase struct {
	name  string
	query func() (rod.Elements, error)
	texts []string
}

func TestElementsCollectionContexts(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(queryContextHTML)).Timeout(15 * time.Second)
	defer p.CancelTimeout()
	p.MustWaitLoad()
	frame := p.MustElement("iframe").MustFrame()
	frameWindow, err := frame.Evaluate(rod.Eval(`() => window`).ByObject())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { g.E(frame.Release(frameWindow)) }()
	// Adoption changes the document but retains the node's original JavaScript
	// prototype realm. Exercise both properties instead of assuming they match.
	adopted := p.MustEval(`() => {
		const node = frames[0].document.querySelector('#adopted');
		document.body.append(document.adoptNode(node));
		return node.ownerDocument === document &&
			node instanceof frames[0].HTMLElement && !(node instanceof HTMLElement);
	}`)
	if !adopted.Bool() {
		t.Fatal("fixture did not retain the adopted node's original prototype realm")
	}
	var contextLookups atomic.Int64
	g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
		if request, ok := params.(proto.RuntimeCallFunctionOn); ok && request.FunctionDeclaration == `() => window` {
			contextLookups.Add(1)
		}
		return g.mc.principal.Call(ctx, session, method, params)
	})
	defer g.mc.resetCall()
	tests := []queryContextCase{
		{
			name: "empty collection",
			query: func() (rod.Elements, error) {
				return p.ElementsByJS(rod.Eval(`() => []`))
			},
		},
		{
			name: "iframe collection returned to parent",
			query: func() (rod.Elements, error) {
				return p.ElementsByJS(rod.Eval(`() => frames[0].document.querySelectorAll('section')`))
			},
			texts: []string{"Child"},
		},
		{
			name: "explicit iframe execution context",
			query: func() (rod.Elements, error) {
				return p.ElementsByJS(rod.Eval(`() => document.querySelectorAll('section')`).This(frameWindow))
			},
			texts: []string{"Child"},
		},
		{
			name: "mixed frame array",
			query: func() (rod.Elements, error) {
				return p.ElementsByJS(rod.Eval(`() => [
					frames[0].document.querySelector('#child'),
					document.querySelector('#main'),
					document.querySelector('#adopted')
				]`))
			},
			texts: []string{"Child", "Main", "Adopted"},
		},
		{
			name: "mixed frame array evaluated in child",
			query: func() (rod.Elements, error) {
				return frame.ElementsByJS(rod.Eval(`() => [
					document.querySelector('#child'), parent.document.querySelector('#main')
				]`))
			},
			texts: []string{"Child", "Main"},
		},
		{
			name: "CSS collection with adopted node",
			query: func() (rod.Elements, error) {
				return p.Elements("section")
			},
			texts: []string{"Main", "Adopted"},
		},
		{
			name: "XPath collection with adopted node",
			query: func() (rod.Elements, error) {
				return p.ElementsX("//section")
			},
			texts: []string{"Main", "Adopted"},
		},
		{
			name: "iframe CSS collection",
			query: func() (rod.Elements, error) {
				return frame.Elements("section")
			},
			texts: []string{"Child"},
		},
		{
			name: "iframe XPath collection",
			query: func() (rod.Elements, error) {
				return frame.ElementsX("//section")
			},
			texts: []string{"Child"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := contextLookups.Load()
			elements, err := test.query()
			if err != nil {
				t.Fatal(err)
			}
			if len(elements) != len(test.texts) {
				t.Fatalf("element count = %d, want %d", len(elements), len(test.texts))
			}
			wantLookups := int64(0)
			if len(elements) > 0 {
				wantLookups = 1
			}
			if count := contextLookups.Load() - before; count != wantLookups {
				t.Fatalf("context lookups = %d for %d elements, want %d", count, len(elements), wantLookups)
			}
			for i, element := range elements {
				assertElementCollectionContext(t, element, test.texts[i])
			}
		})
	}
	t.Run("large collection shares context lookup", func(t *testing.T) {
		before := contextLookups.Load()
		elements, err := p.ElementsByJS(rod.Eval(`() => Array(100).fill(document.querySelector('#main'))`))
		if err != nil {
			t.Fatal(err)
		}
		if len(elements) != 100 {
			t.Fatalf("element count = %d, want 100", len(elements))
		}
		if count := contextLookups.Load() - before; count != 1 {
			t.Fatalf("context lookups = %d for 100 elements, want 1", count)
		}
		assertElementCollectionContext(t, elements.First(), "Main")
		assertElementCollectionContext(t, elements.Last(), "Main")
	})
}

func assertElementCollectionContext(t *testing.T, element *rod.Element, want string) {
	t.Helper()
	if text, err := element.Text(); err != nil || text != want {
		t.Fatalf("element text = %q, %v; want %q", text, err, want)
	}
	value, err := element.Eval(`() => this.textContent`)
	if err != nil {
		t.Fatal(err)
	}
	if value.Value.Str() != want {
		t.Fatalf("element evaluation = %q, want %q", value.Value.Str(), want)
	}
	css, err := element.Elements(":scope > .label")
	if err != nil {
		t.Fatal(err)
	}
	xpath, err := element.ElementsX("./span[@class='label']")
	if err != nil {
		t.Fatal(err)
	}
	for _, children := range []rod.Elements{css, xpath} {
		if len(children) != 1 {
			t.Fatalf("child count = %d, want 1", len(children))
		}
		if text, err := children[0].Text(); err != nil || text != want {
			t.Fatalf("child text = %q, %v; want %q", text, err, want)
		}
	}
}
