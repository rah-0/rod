// Run with: go run ./examples/json-evaluation
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
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
		<ul id="items"><li data-price="3">Apple</li><li data-price="5">Pear</li></ul>
		<output id="status">waiting</output>`, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if err := f.Page.WaitLoad(); err != nil {
		return err
	}

	// Pass the selector as data. The async function projects DOM elements into
	// ordinary JSON values; EvalJSON awaits its promise before decoding.
	var cart struct {
		Names []string `json:"names"`
		Total int      `json:"total"`
	}
	if err := f.Page.EvalJSON(&cart, `async selector => {
		const items = Array.from(document.querySelectorAll(selector));
		return {
			names: items.map(item => item.textContent),
			total: items.reduce((sum, item) => sum + Number(item.dataset.price), 0)
		};
	}`, "#items li"); err != nil {
		return err
	}

	// Nil awaits side effects and discards even an unsupported return value.
	if err := f.Page.EvalJSON(nil, `() => {
		document.querySelector("#status").textContent = "ready";
		return Symbol("discarded");
	}`); err != nil {
		return err
	}
	var status string
	if err := f.Page.EvalJSON(&status, `() => document.querySelector("#status").textContent`); err != nil {
		return err
	}

	// An undefined property is rejected instead of silently disappearing.
	var value any
	rejection := f.Page.EvalJSON(&value, `() => ({missing: undefined})`)
	var serialization *rod.JSONSerializationError
	if !errors.As(rejection, &serialization) {
		return fmt.Errorf("expected JSONSerializationError, got %v", rejection)
	}
	_, err = fmt.Fprintf(output, "Items: %s; total: %d\nDiscarded result, status: %s\nUndefined property rejected\n",
		strings.Join(cart.Names, ", "), cart.Total, status)
	return err
}
