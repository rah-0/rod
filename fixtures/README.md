# Browser fixtures

This directory contains small pages and assets used by the root browser tests
and examples. Their HTML, JavaScript, and CSS define controlled test conditions;
an old modification date does not pin the browser version. Tests use an installed
browser through the [launcher](../lib/launcher/README.md).

| Files | Purpose |
| --- | --- |
| `click*.html`, `input.html`, `keys.html`, `touch.html`, `double-click.html`, `drag.html`, `mouse-move.html`, `interactable.html` | Pointer, keyboard, form, and interaction behavior. |
| `selector.html`, `describe.html`, `shadow-dom.html`, `iframe.html` | Element queries, DOM inspection, shadow roots, and frame traversal. |
| `blank.html`, `open-page*.html`, `alert.html`, `prevent-close.html` | Navigation, new pages, dialogs, and close handling. |
| `wait-stable.html`, `page-wait-stable.html`, `wait_elements.html`, `slow-render.html` | Animation, delayed rendering, and readiness conditions. |
| `scroll.html`, `scroll-y.html`, `canvas.html`, `resource.html`, `icon.png` | Scrolling, capture, canvas content, and resource loading. |
| `add-script-tag.js`, `add-style-tag.css` | Injected script and stylesheet behavior. |
| `fetch.html` | Network interception. Its `/a` and `/b` endpoints are supplied by [the hijack tests](../hijack_test.go), not files on disk. |
| `worker.html`, `worker.js` | A dedicated worker served over local HTTP; `TestWorkerEcho` checks the echoed message in the page. |
| `fonts.html` | A maintained multilingual text and emoji sample. Edit it directly. `TestFonts` checks PDF generation with the installed browser; it does not verify glyph appearance. |
| `chrome-extension/` | The content-script fixture used by the [extension example](../examples/load-extension/main.go). |

Other fixtures live beside their consumers. [CDP fixtures](../lib/cdp/fixtures)
are relative to the `lib/cdp` package, and the
[Docker client fixture](../lib/docker/fixtures/manager-client) belongs to the
Docker integration tests. Tests also define HTML and HTTP responses inline;
the [HTTP fixture package](../lib/fixture/README.md) serves these pages on
loopback. A feature test does not need a corresponding file in this directory.

The [capture example](../examples/capture/main.go) creates `page.png`,
`element.png`, `mobile.png`, and `page.pdf` in the temporary directory it prints,
or in the directory selected by `-out`. Those are generated artifacts rather
than checked-in fixtures. Capture tests use temporary directories for their
outputs; see the [root test instructions](../README.md#development) for retained
failure artifacts.

## Artwork

[design.sketch](design.sketch) and [icon.pxd](icon.pxd) are editable design
sources. [icon.png](icon.png) is also used as a browser resource, while
[object-model.svg](object-model.svg) is an exported object-model diagram.
Keep design sources and exported artwork separate from runtime test outputs.

## Checks

From the repository root, with an installed browser:

```sh
bash scripts/check.sh browser
```

This runs the installed-browser protocol check and browser tests, including
the runnable example tests. For a focused change, run the relevant Go test or
example package with the race and coverage flags documented in the
[root README](../README.md#development).
