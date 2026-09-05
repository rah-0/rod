// Package main ...
package main

import "testing"

const appHTML = `<main>
<div class="component-display">0</div>
<button>1</button><button>2</button><button>3</button>
<button>+</button><button>x</button><button>=</button>
</main>
<script>
let expression = "";
const display = document.querySelector(".component-display");
for (const button of document.querySelectorAll("button")) {
  button.addEventListener("click", () => {
    if (button.textContent !== "=") {
      expression += button.textContent;
      return;
    }
    const parts = expression.match(/^(\d+)([+x])(\d+)$/);
    const left = Number(parts[1]);
    const right = Number(parts[3]);
    display.textContent = String(parts[2] === "+" ? left + right : left * right);
    expression = "";
  });
}
</script>`

// test case: 1 + 2 = 3.
func TestAdd(t *testing.T) {
	g := setup(t)

	p := g.page(appHTML)

	p.MustElementR("button", "1").MustClick()
	p.MustElementR("button", `^\+$`).MustClick()
	p.MustElementR("button", "2").MustClick()
	p.MustElementR("button", "=").MustClick()

	// assert the result with t.Eq
	g.Eq(p.MustElement(".component-display").MustText(), "3")
}

// test case: 2 * 3 = 6.
func TestMultiple(t *testing.T) {
	g := setup(t)

	p := g.page(appHTML)

	// use for-loop to click each button
	for _, regex := range []string{"2", "x", "3", "="} {
		p.MustElementR("button", regex).MustClick()
	}

	g.Eq(p.MustElement(".component-display").MustText(), "6")
}
