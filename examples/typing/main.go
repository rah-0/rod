// Run with: go run ./examples/typing
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/launcher"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

type State struct {
	Value     string `json:"value"`
	Focused   bool   `json:"focused"`
	KeyDowns  int    `json:"keydowns"`
	KeyUps    int    `json:"keyups"`
	Submitted bool   `json:"submitted"`
}

func run(ctx context.Context, output io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	browser := rod.New().ControlURL("").Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.CloseWithTimeout(5*time.Second)) }()
	f, err := fixture.HTML(browser, `<!doctype html>
		<input id="message" value="existing:">
		<script>
			window.demo = {keydowns: 0, keyups: 0, submitted: false};
			const field = document.querySelector("#message");
			field.addEventListener("keydown", event => {
				window.demo.keydowns++;
				if (event.code === "Enter") window.demo.submitted = true;
			});
			field.addEventListener("keyup", () => window.demo.keyups++);
		</script>`, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if err := f.Page.WaitLoad(); err != nil {
		return err
	}
	const readState = `() => ({
		value: document.querySelector("#message").value,
		focused: document.activeElement.id === "message",
		...window.demo
	})`

	// Validate the entire string before focusing or typing. Unsupported text
	// returns no usable prefix and never falls back to text insertion.
	keys, rejection := input.Keys("valid prefix 🦊")
	if !errors.Is(rejection, input.ErrUnsupportedCharacter) || keys != nil {
		return fmt.Errorf("expected unsupported text without partial keys, got %v, %v", keys, rejection)
	}
	var before State
	if err := f.Page.EvalJSON(&before, readState); err != nil {
		return err
	}

	// Shifted characters use Rod's key mapping; line feed dispatches Enter.
	keys, err = input.Keys("Go!\n")
	if err != nil {
		return err
	}
	element, err := f.Page.Element("#message")
	if err != nil {
		return err
	}
	// Place the caret after existing text before Element.Type focuses it.
	if _, err := element.Eval(`() => this.setSelectionRange(this.value.length, this.value.length)`); err != nil {
		return err
	}
	if err := element.Type(keys...); err != nil {
		return err
	}
	var typed State
	if err := f.Page.EvalJSON(&typed, readState); err != nil {
		return err
	}

	// Unicode insertion is an explicit alternative. Selecting first replaces
	// the existing value; these operations do not dispatch keyboard events.
	if err := element.SelectAllText(); err != nil {
		return err
	}
	if err := element.Input("こんにちは 🦊"); err != nil {
		return err
	}
	var inserted State
	if err := f.Page.EvalJSON(&inserted, readState); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output,
		"Rejected text: value=%s, focused=%t, keys=%d/%d\nTyped: %s, keys=%d/%d, submitted=%t\nInserted: %s, keys=%d/%d\n",
		before.Value, before.Focused, before.KeyDowns, before.KeyUps,
		typed.Value, typed.KeyDowns, typed.KeyUps, typed.Submitted,
		inserted.Value, inserted.KeyDowns, inserted.KeyUps)
	return err
}
