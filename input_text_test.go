package rod_test

import (
	"testing"
	"time"

	"github.com/rah-0/rod/lib/input"
)

func TestInputKeysBrowser(t *testing.T) {
	g := setup(t)
	p := g.newPage(g.html(`<!doctype html><input id="text" value="prefix:"><script>
		window.events = [];
		window.actions = 0;
		window.focuses = 0;
		const input = document.querySelector("input");
		input.addEventListener("focus", () => window.focuses++);
		for (const type of ["keydown", "keyup"]) {
			input.addEventListener(type, event => {
				window.events.push([event.type, event.key, event.code]);
				if (type === "keydown" && event.code === "Enter") window.actions++;
			});
		}
	</script>`)).Timeout(10 * time.Second)
	defer p.CancelTimeout()
	g.E(p.WaitLoad())
	el := p.MustElement("#text")

	keys, err := input.Keys("valid prefixĈ")
	g.Err(err)
	g.Nil(keys)
	g.Eq(p.MustEval(`() => window.events.length`).Int(), 0)
	g.Eq(p.MustEval(`() => window.focuses`).Int(), 0)
	g.Eq(el.MustProperty("value").Str(), "prefix:")

	keys, err = input.Keys("aA!?\n")
	g.E(err)
	// Put the caret after existing content, as an application or user may do.
	el.MustEval(`() => this.setSelectionRange(this.value.length, this.value.length)`)
	g.E(el.Type(keys...))
	g.Eq(el.MustProperty("value").Str(), "prefix:aA!?")
	g.Eq(p.MustEval(`() => window.actions`).Int(), 1)
	var events [][]string
	g.E(p.EvalJSON(&events, `() => window.events`))
	g.Eq(events, [][]string{
		{"keydown", "a", "KeyA"}, {"keyup", "a", "KeyA"},
		{"keydown", "A", "KeyA"}, {"keyup", "A", "KeyA"},
		{"keydown", "!", "Digit1"}, {"keyup", "!", "Digit1"},
		{"keydown", "?", "Slash"}, {"keyup", "?", "Slash"},
		{"keydown", "Enter", "Enter"}, {"keyup", "Enter", "Enter"},
	})

	g.E(el.SelectAllText())
	g.E(el.Input("こんにちは🦊"))
	g.Eq(el.MustProperty("value").Str(), "こんにちは🦊")
	g.Eq(p.MustEval(`() => window.events.length`).Int(), len(events))
}
