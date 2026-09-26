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
	// checks counts calls testing which cached context owns the first element.
	checks int64
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
	// Every context is already cached: queries must not create window handles, and
	// only a call on an object of unknown context checks the cached contexts.
	var windows, checks atomic.Int64
	g.mc.setCall(func(ctx context.Context, session, method string, params any) ([]byte, error) {
		if request, ok := params.(proto.RuntimeCallFunctionOn); ok {
			switch {
			case request.FunctionDeclaration == `() => window`:
				windows.Add(1)
			case request.FunctionDeclaration == `function() {}` && len(request.Arguments) == 1:
				checks.Add(1)
			}
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
			// The page window rejects the frame's handle; the frame window accepts it.
			checks: 2,
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
			windowsBefore, checksBefore := windows.Load(), checks.Load()
			elements, err := test.query()
			if err != nil {
				t.Fatal(err)
			}
			if len(elements) != len(test.texts) {
				t.Fatalf("element count = %d, want %d", len(elements), len(test.texts))
			}
			if count := windows.Load() - windowsBefore; count != 0 {
				t.Fatalf("created %d window handles for cached contexts", count)
			}
			if count := checks.Load() - checksBefore; count != test.checks {
				t.Fatalf("context checks = %d for %d elements, want %d", count, len(elements), test.checks)
			}
			for i, element := range elements {
				assertElementCollectionContext(t, element, test.texts[i])
			}
		})
	}
	t.Run("large collection shares the collection context", func(t *testing.T) {
		before := windows.Load() + checks.Load()
		elements, err := p.ElementsByJS(rod.Eval(`() => Array(100).fill(document.querySelector('#main'))`))
		if err != nil {
			t.Fatal(err)
		}
		if len(elements) != 100 {
			t.Fatalf("element count = %d, want 100", len(elements))
		}
		if count := windows.Load() + checks.Load() - before; count != 0 {
			t.Fatalf("context lookup calls = %d for 100 elements, want 0", count)
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
