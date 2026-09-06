// Run with: go test -count=1 -race -cover -covermode=atomic ./examples/e2e-testing/...
package main

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

// Share one process while giving each test its own browser context and fixture.
func TestCalculator(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	browser := rod.New().Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := browser.CloseWithTimeout(5 * time.Second); err != nil {
			t.Error(err)
		}
	}()

	for _, test := range []struct {
		Name    string
		Buttons []string
		Want    string
	}{
		{"addition", []string{"1", "+", "2", "="}, "3"},
		{"multiplication", []string{"2", "x", "3", "="}, "6"},
	} {
		t.Run(test.Name, func(t *testing.T) {
			isolated, err := browser.Context(t.Context()).Incognito()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				// Test contexts expire before cleanup; use an independent budget.
				if err := isolated.CloseWithTimeout(5 * time.Second); err != nil {
					t.Error(err)
				}
			})
			app, err := fixture.HTML(isolated, calculatorHTML, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := app.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := app.Page.WaitLoad(); err != nil {
				t.Fatal(err)
			}
			for _, label := range test.Buttons {
				button, err := app.Page.ElementR("button", "^"+regexp.QuoteMeta(label)+"$")
				if err != nil {
					t.Fatal(err)
				}
				if err := button.Click(proto.InputMouseButtonLeft, 1); err != nil {
					t.Fatal(err)
				}
			}
			display, err := app.Page.Element("output")
			if err != nil {
				t.Fatal(err)
			}
			got, err := display.Text()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.Want {
				t.Fatalf("calculator displayed %q, want %q", got, test.Want)
			}
		})
	}
}

const calculatorHTML = `<!doctype html>
<output>0</output>
<button>1</button><button>2</button><button>3</button>
<button>+</button><button>x</button><button>=</button>
<script>
let expression = '';
for (const button of document.querySelectorAll('button')) {
    button.addEventListener('click', () => {
        if (button.textContent !== '=') {
            expression += button.textContent;
            return;
        }
        const [, left, operator, right] = expression.match(/^(\d+)([+x])(\d+)$/);
        document.querySelector('output').textContent = operator === '+'
            ? Number(left) + Number(right) : Number(left) * Number(right);
        expression = '';
    });
}
</script>`
