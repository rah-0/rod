# Selecting elements and waiting

Run from the repository root:

```sh
go run ./examples/query-selection
```

Expected output:

```text
Optional notice: false
CSS items: 2
Text match: Beta
Scoped button: Start
Search result: Alpha
Frame text: Frame content
Shadow text: Shadow content
Race outcome: Ready
Missing element: deadline exceeded
```

The [example](main.go) launches its own browser and serves the embedded
[page](page.html) locally. It
demonstrates optional matches, CSS lists, text matching, JavaScript arguments,
relative XPath, DOM search cleanup, iframe and shadow-root queries, alternative
outcomes, and a query deadline. It uses `WaitInteractive` for document parsing
and an explicit query for the delayed application result.

Choose a query according to whether absence is expected. A page's single-element
queries wait for a match by default; an optional check or list query returns the
current result. Finding an element does not establish that it is visible or ready
for interaction.

## Choose a query

These are the defaults implemented in [query.go](../../query.go):

| Page method | Selection | When no element matches |
| --- | --- | --- |
| `Element(selector)` | First CSS match. | Retries until a match, cancellation, or another error. |
| `ElementX(xpath)` | First XPath match. | Retries. |
| `ElementR(selector, regex)` | First CSS match whose text matches a JavaScript regular expression. | Retries. |
| `ElementByJS(rod.Eval(function, args...))` | DOM node returned by a JavaScript function. | Retries when the function returns `null`. Other non-node values, including `undefined`, return `ExpectElementError` after their remote object is released. |
| `Elements(selector)` / `ElementsX(xpath)` | Current CSS / XPath matches. | Returns an empty list without retrying for a match. |
| `Has(selector)` / `HasX(xpath)` / `HasR(selector, regex)` | One optional match. | Returns `false, nil, nil` without retrying for a match. |
| `Search(query)` | Browser DOM search using text, CSS, or XPath. | Retries until results are available. Returns a `SearchResult` that needs `Release`. |
| `Race().Element(...).ElementX(...).Do()` | First successful branch. | Rechecks branches until a match, cancellation, or another error. |

The receiver matters: **single-element queries on an `Element` do not retry**.
`container.Element`, `ElementX`, `ElementR`, and `ElementByJS` return an
`ElementNotFoundError` on absence. Its `Has` methods turn that error into
`false, nil, nil`. Its list methods return an empty list. To wait for a descendant,
use a scoped selector on the page, such as `page.Element("#panel button")`, or a
page-level `ElementByJS` that returns the desired descendant or `null`.

"Without retrying" means no polling for a matching element; the browser request
still takes time and can fail. Invalid selectors and JavaScript errors are errors,
not absence. `ElementR` uses browser-side regular expressions, not Go's `regexp`.

## Optional matches and bounded waits

For a connected `page`, handle an optional element explicitly:

```go
found, notice, err := page.Has("#notice")
if err != nil {
    return err
}
if found {
    text, err := notice.Text()
    if err != nil {
        return err
    }
    fmt.Println(text)
}
```

Give required queries a deadline. `Context` returns a view; it does not change the
original page's context. The deadline covers every operation using that view,
including operations on elements obtained from it:

```go
ctx, cancel := context.WithTimeout(page.GetContext(), 5*time.Second)
defer cancel()

element, err := page.Context(ctx).Element("#result")
if errors.Is(err, context.DeadlineExceeded) {
    return fmt.Errorf("result did not appear: %w", err)
}
if err != nil {
    return err
}
text, err := element.Text()
```

Use `errors.Is(err, context.Canceled)` to recognize explicit cancellation.
Returned elements retain the query context; to keep using one after canceling
that context, create a view with `element.Context(parentContext)` while its
document is still valid. `page.Timeout(duration)` is a convenience alternative;
keep the returned view and defer its `CancelTimeout()`.

To make a page single-element query attempt once, use
`page.Sleeper(rod.NotFoundSleeper).Element(selector)` and check
`errors.Is(err, &rod.ElementNotFoundError{})`. Without a deadline or a sleeper
that stops, an absent required element can wait indefinitely.

## Race alternative outcomes

```go
ctx, cancel := context.WithTimeout(page.GetContext(), 5*time.Second)
defer cancel()

winner, err := page.Context(ctx).Race().
    Element("#success").
    Element("#failure").
    Do()
```

A race checks branches in registration order on each retry; the first successful
branch wins. It does not run those queries concurrently. An error other than
`ElementNotFoundError` stops the race. `Handle` attaches a callback to the preceding
branch, runs it only for the winner, and propagates its error. Custom `ElementFunc`
callbacks must use the supplied page/context and return promptly; a Go callback
that ignores cancellation cannot be interrupted by the race's deadline.

## JavaScript arguments and scoped queries

Pass JavaScript functions to `rod.Eval`, and send values as arguments instead of
inserting them into JavaScript source:

```go
element, err := page.ElementByJS(rod.Eval(
    `id => document.getElementById(id)`, "item's-details",
))
```

`ElementByJS` calls the function again while it returns `null`, so keep the query
free of side effects. Use `.ByPromise()` on the options when the function returns
a promise. For `container.ElementByJS`, `this` is the container:

```go
child, err := container.ElementByJS(rod.Eval(
    `selector => this.querySelector(selector)`, "button",
))
```

CSS queries on an element search its descendants. For XPath, use `./button` for
direct children or `.//button` for descendants. A leading `//button` starts at the
document root even when called on an element. These selectors are passed as
arguments by the [query helpers](../../lib/js/helper.js).

## Iframes, shadow roots, and DOM search

Ordinary CSS/XPath queries stay in their document or root; they do not descend
into iframe documents or through shadow roots. Query the iframe element, call
`Frame`, then query the returned page. For a shadow host, call `ShadowRoot`, then
query the returned element. Check errors at each step; a missing shadow root
returns `NoShadowRootError`. Shadow-root descendant queries retain the one-attempt
behavior of element queries. Use CSS inside shadow roots.

[`Element.Frame`](../../element.go) supports out-of-process iframes by attaching
their renderer session, including nested frames. A frame view keeps its session;
after a renderer transition, obtain a new view with `Frame`. An obsolete view
reports a session error or `ErrFrameContextChanged`. See the
[frame lifecycle contract](../../doc/BREAKING.md#v01200--compared-with-v01190) and
[cross-process frame test](../../tests/frame_session_test.go).

`Search` uses the DOM tree exposed by its CDP session. It can find nodes in nested
same-session iframe documents and shadow trees, including browser shadow DOM.
It is not scoped to a particular element or necessarily to the document of a
same-session frame view. It does not attach other renderer sessions to search
them; use `Frame` to query an out-of-process iframe explicitly.

Release every successful search result after reading `First`, `Get`, or `All`:

```go
result, err := page.Search("button")
if err != nil {
    return err
}
elements, readErr := result.All()
if err := errors.Join(readErr, result.Release()); err != nil {
    return err
}
```

`Release` frees the remote search result, not the elements already obtained.
Failed searches clean up themselves. `MustSearch` returns only the first element
and releases its search result automatically; a race's `Search` branch also
releases its search result.

## Document and application readiness

`Page.WaitInteractive` returns when `document.readyState` is `interactive` or
`complete`: parsing has finished, but images and other subresources may still be
loading. It also works after that state is reached and on frame pages. Navigation
during the wait makes it check the replacement document. A page context bounds
the wait; `MustWaitInteractive` uses the same behavior and panics on errors.

Use `Page.WaitLoad` for the full load event, or `Page.WaitDOMStable` for a period
of DOM stability. None of these establishes that an application has finished its
asynchronous work. Wait for the actual condition with a required element query,
`Page.Wait`, or a relevant event. An element query does not wait for visibility;
call `Element.WaitVisible` when visibility is the condition you need.

## Errors and verification

`Must` helpers use the same selection and retry rules and panic on errors by
default. `MustHas` still returns `false` for absence, and `MustElements` returns an
empty list. Use the error-returning forms when timeout, absence, or malformed
input is an expected outcome.

The [example test](main_test.go) runs the same workflow and checks its results.
The [query tests](../../tests/query_test.go),
[evaluation regression tests](../../tests/page_eval_regression_test.go), and
[readiness tests](../../tests/page_wait_interactive_test.go) cover the underlying
contracts and failure paths.
