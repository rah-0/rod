package rod_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// This example opens a local page, searches for "git",
// and then gets the result element which gives the description for Git.
func Example_basic() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html>
<html>
<head><title>Repository search</title></head>
<body>
<input id="query-builder-test" name="query">
<span id="result"></span>
<script>
const query = document.querySelector("#query-builder-test")
document.addEventListener("keydown", event => {
	if (event.key === "/") {
		event.preventDefault()
		query.focus()
	}
})
query.addEventListener("keydown", event => {
	if (event.key === "Enter" && query.value.toLowerCase() === "git") {
		document.querySelector("#result").textContent = "Git is the most widely used version control system."
		document.title = "Repository search results"
	}
})
</script>
</body>
</html>`)
	}))
	defer server.Close()

	// Launch a new browser with default options, and connect to it.
	browser := rod.New().MustConnect()

	// Close the implicitly launched browser when this function returns.
	defer browser.MustClose()

	// Create a new page
	page := browser.MustPage(server.URL).MustWaitStable()

	// Trigger the search input with hotkey "/"
	page.Keyboard.MustType(input.Slash)

	// We use css selector to get the search input element and input "git"
	page.MustElement("#query-builder-test").MustInput("git").MustType(input.Enter)

	// Wait until css selector get the element then get the text content of it.
	text := page.MustElementR("span", "most widely used").MustText()

	fmt.Println(text)

	// Get all input elements. Rod supports query elements by css selector, xpath, and regex.
	// For more detailed usage, check the query_test.go file.
	fmt.Println("Found", len(page.MustElements("input")), "input elements")

	// Eval js on the page
	page.MustEval(`() => console.log("hello world")`)

	// Pass parameters as json objects to the js function. This MustEval will result 3
	fmt.Println("1 + 2 =", page.MustEval(`(a, b) => a + b`, 1, 2).Int())

	// When eval on an element, "this" in the js is the current DOM element.
	fmt.Println(page.MustElement("title").MustEval(`() => this.innerText`).String())

	// Output:
	// Git is the most widely used version control system.
	// Found 1 input elements
	// 1 + 2 = 3
	// Repository search results
}

// Shows how to disable headless mode and debug.
// Rod provides a lot of debug options, you can set them with setter methods or command-line defaults.
// Doc for command-line defaults: https://pkg.go.dev/github.com/rah-0/rod/lib/defaults
func Example_disable_headless_to_debug() {
	// Headless runs the browser on foreground, you can also use flag "-rod=show"
	// Devtools opens the tab in each new tab opened automatically
	l := launcher.New().
		Headless(false).
		Devtools(true)

	defer func() {
		l.Kill()
		l.Cleanup()
	}()
	url := l.MustLaunch()

	// Trace shows verbose debug information for each action executed
	// SlowMotion is a debug related function that waits 2 seconds between
	// each action, making it easier to inspect what your code is doing.
	browser := rod.New().
		ControlURL(url).
		Trace(true).
		SlowMotion(2 * time.Second).
		MustConnect()

	// ServeMonitor plays screenshots of each tab. This feature is extremely
	// useful when debugging with headless mode.
	// You can also enable it with flag "-rod=monitor"
	launcher.Open(browser.ServeMonitor(""))

	defer browser.MustClose()

	page := browser.MustPage("https://github.com/")

	page.MustElement("input").MustInput("git").MustType(input.Enter)

	text := page.MustElement(".codesearch-results p").MustText()

	fmt.Println(text)

	utils.Pause() // pause goroutine
}

// Rod use https://golang.org/pkg/context to handle cancellations for IO blocking operations, most times it's timeout.
// Context will be recursively passed to all sub-methods.
// For example, methods like Page.Context(ctx) will return a clone of the page with the ctx,
// all the methods of the returned page will use the ctx if they have IO blocking operations.
// [Page.Timeout] or [Page.WithCancel] is just a shortcut for Page.Context.
// Of course, Browser or Element works the same way.
func Example_context_and_timeout() {
	browser := rod.New().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage("https://github.com")

	page.
		// Set a 5-second timeout for all chained methods
		Timeout(5 * time.Second).

		// The total time for MustWaitLoad and MustElement must be less than 5 seconds
		MustWaitLoad().
		MustElement("title").

		// Methods after CancelTimeout won't be affected by the 5-second timeout
		CancelTimeout().

		// Set a 10-second timeout for all chained methods
		Timeout(10 * time.Second).

		// Panics if it takes more than 10 seconds
		MustText()

	// The two code blocks below are basically the same:
	{
		page.Timeout(5 * time.Second).MustElement("a").CancelTimeout()
	}
	{
		// Use this way you can customize your own way to cancel long-running task
		page, cancel := page.WithCancel()
		go func() {
			time.Sleep(time.Duration(rand.Int())) // cancel after randomly time
			cancel()
		}()
		page.MustElement("a")
	}
}

func Example_context_and_EachEvent() {
	browser := rod.New().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage("https://github.com").MustWaitLoad()

	page, cancel := page.WithCancel()

	go func() {
		time.Sleep(time.Second)
		cancel()
	}()

	// It's a blocking method, it will wait until the context is cancelled
	err := page.EachEvent(rod.On(func(_ *proto.PageLifecycleEvent, _ proto.TargetSessionID) bool { return false }))()

	if errors.Is(err, context.Canceled) {
		fmt.Println("cancelled")
	} else {
		utils.E(err)
	}
}

// We use "Must" prefixed functions to write example code. But in production you may want to use
// the no-prefix version of them.
// About why we use "Must" as the prefix, it's similar to https://golang.org/pkg/regexp/#MustCompile
func Example_error_handling() {
	browser := rod.New().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage("https://mdn.dev")

	// We use Go's standard way to check error types, no magic.
	check := func(err error) {
		if errors.Is(err, context.DeadlineExceeded) { // timeout error
			fmt.Println("timeout err")
		} else if evalErr, ok := errors.AsType[*rod.EvalError](err); ok { // eval error
			fmt.Println(evalErr.LineNumber)
		} else if err != nil {
			fmt.Println("can't handle", err)
		}
	}

	// The two code blocks below are doing the same thing in two styles:

	// The block below is better for debugging or quick scripting. We use panic to short-circuit logics.
	// So that we can take advantage of fluent interface (https://en.wikipedia.org/wiki/Fluent_interface)
	// and fail-fast (https://en.wikipedia.org/wiki/Fail-fast).
	// This style will reduce code, but it may also catch extra errors (less consistent and precise).
	{
		err := rod.Try(func() {
			fmt.Println(page.MustElement("a").MustHTML()) // use "Must" prefixed functions
		})
		check(err)
	}

	// The block below is better for production code. It's the standard way to handle errors.
	// Usually, this style is more consistent and precise.
	{
		el, err := page.Element("a")
		if err != nil {
			check(err)
			return
		}
		html, err := el.HTML()
		if err != nil {
			check(err)
			return
		}
		fmt.Println(html)
	}
}

// Example_search shows how to use Search to get element inside nested iframes or shadow DOMs.
// It works the same as https://developers.google.com/web/tools/chrome-devtools/dom#search
func Example_search() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/outer":
			_, _ = fmt.Fprint(w, `<iframe src="/inner"></iframe>`)
		case "/inner":
			_, _ = fmt.Fprint(w, `<div id="host"></div><script>
const shadow = document.querySelector("#host").attachShadow({mode: "open"})
const button = document.createElement("button")
button.id = "activate"
button.textContent = "Activate"
button.onclick = () => { top.document.title = "Activated" }
shadow.append(button)
</script>`)
		default:
			_, _ = fmt.Fprint(w, `<iframe src="/outer"></iframe>`)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	browser := rod.New().Context(ctx).NoDefaultDevice().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage(server.URL).MustWaitLoad()

	// Search crosses both iframe boundaries and finds the shadow DOM button.
	page.MustSearch("#activate").MustClick()
	page.MustWait(`() => document.title === "Activated"`)

	fmt.Println(page.MustInfo().Title)

	// Output: Activated
}

func Example_page_screenshot() {
	browser := rod.New().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage("https://github.com").MustWaitLoad()

	// simple version
	page.MustScreenshot("my.png")

	// customization version
	img, _ := page.Screenshot(true, &proto.PageCaptureScreenshot{
		Format:  proto.PageCaptureScreenshotFormatJpeg,
		Quality: new(90),
		Clip: &proto.PageViewport{
			X:      0,
			Y:      0,
			Width:  300,
			Height: 200,
			Scale:  1,
		},
		FromSurface: new(true),
	})
	_ = utils.OutputFile("my.jpg", img)
}

func Example_page_scroll_screenshot() {
	browser := rod.New().MustConnect()
	defer browser.MustClose()

	// capture entire browser viewport, returning jpg with quality=90
	img, err := browser.MustPage("https://desktop.github.com/").MustWaitStable().ScrollScreenshot(&rod.ScrollScreenshotOptions{
		Format:  proto.PageCaptureScreenshotFormatJpeg,
		Quality: new(90),
	})
	if err != nil {
		panic(err)
	}

	_ = utils.OutputFile("my.jpg", img)
}

func Example_page_pdf() {
	browser := rod.New().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage("https://github.com").MustWaitLoad()

	// simple version
	page.MustPDF("my.pdf")

	// customized version
	pdf, _ := page.PDF(&proto.PagePrintToPDF{
		PaperWidth:  new(8.5),
		PaperHeight: new(float64(11)),
		PageRanges:  "1-3",
	})
	_ = utils.OutputFile("my.pdf", pdf)
}

// Show how to handle multiple results of an action.
// Such as when you login a page, the result can be success or wrong password.
func Example_race_selectors() {
	const username = ""
	const password = ""

	browser := rod.New().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage("https://leetcode.com/accounts/login/")

	page.MustElement("#id_login").MustInput(username)
	page.MustElement("#id_password").MustInput(password).MustType(input.Enter)

	// It will keep retrying until one selector has found a match
	elm := page.Race().Element(".nav-user-icon-base").MustHandle(func(e *rod.Element) {
		// print the username after successful login
		fmt.Println(*e.MustAttribute("title"))
	}).Element("[data-cy=sign-in-error]").MustDo()

	if elm.MustMatches("[data-cy=sign-in-error]") {
		// when wrong username or password
		panic(elm.MustText())
	}
}

// Rod uses mouse cursor to simulate clicks, so if a button is moving because of animation, the click may not work as expected.
// We usually use WaitStable to make sure the target isn't changing anymore.
func Example_wait_for_animation() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	browser := rod.New().Context(ctx).NoDefaultDevice().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage().MustSetDocumentContent(`<!doctype html>
<button id="open">Open dialog</button>
<section id="dialog" hidden style="position: absolute; top: 80px; left: 0">
  <button onclick="this.parentElement.hidden = true">Close</button>
</section>
<script>
document.querySelector("#open").onclick = () => {
  const dialog = document.querySelector("#dialog")
  dialog.hidden = false
  dialog.animate([{transform: "translateX(0)"}, {transform: "translateX(120px)"}],
                 {duration: 400, fill: "forwards"})
}
</script>`)

	page.MustElement("#open").MustClick()

	closeButton := page.MustElementR("#dialog button", "Close")

	// Here, WaitStable will wait until the button's position and size become stable.
	closeButton.MustWaitStable().MustClick().MustWaitInvisible()

	fmt.Println("done")

	// Output: done
}

// When you want to wait for an ajax request to complete, this example will be useful.
func Example_wait_for_request() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/suggestions" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `["Common Lisp", "Scheme"]`)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html>
<input id="search"><ul id="suggestions"></ul>
<script>
document.querySelector("#search").oninput = async event => {
  const response = await fetch("/suggestions?q=" + encodeURIComponent(event.target.value))
  const suggestions = await response.json()
  document.querySelector("#suggestions").replaceChildren(...suggestions.map(text => {
    const item = document.createElement("li")
    item.textContent = text
    return item
  }))
}
</script>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	browser := rod.New().Context(ctx).NoDefaultDevice().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage(server.URL).MustWaitLoad()

	// Start to analyze request events
	wait := page.MustWaitRequestIdle()

	// This will trigger the search ajax request
	page.MustElement("#search").MustInput("lisp")

	// Wait until there's no active requests
	wait()

	// The response supplies both autocomplete suggestions.
	fmt.Println(page.MustElements("#suggestions li").First().MustText())
	fmt.Println(len(page.MustElements("#suggestions li")))

	// Output:
	// Common Lisp
	// 2
}

// Shows how to change the retry/polling options that is used to query elements.
// This is useful when you want to customize the element query retry logic.
func Example_customize_retry_strategy() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	browser := rod.New().Context(ctx).NoDefaultDevice().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage().MustSetDocumentContent(`<input name="query">`)

	// Each query gets its own retry count and a delay that respects cancellation.
	sleeper := func() utils.Sleeper {
		return utils.EachSleepers(
			utils.CountSleeper(3),
			utils.BackoffSleeper(10*time.Millisecond, 100*time.Millisecond, nil),
		)
	}
	el, err := page.Sleeper(sleeper).Element("input")
	utils.E(err)
	fmt.Println(el.MustProperty("name"))

	_, err = page.Sleeper(sleeper).Element("#missing")
	if limit, ok := errors.AsType[*utils.MaxSleepCountError](err); ok {
		fmt.Println("retry limit:", limit.Max)
	} else {
		utils.E(err)
	}

	// NotFoundSleeper returns an ElementNotFoundError without retrying.
	_, err = page.Sleeper(rod.NotFoundSleeper).Element("#missing")
	if errors.Is(err, &rod.ElementNotFoundError{}) {
		fmt.Println("element not found")
	} else if err != nil {
		panic(err)
	}

	// Output:
	// query
	// retry limit: 3
	// element not found
}

// Shows how we can further customize the browser with the launcher library.
// Usually you use launcher lib to set the browser's command line flags (switches).
// Doc for flags: https://peter.sh/experiments/chromium-command-line-switches
func Example_customize_browser_launch() {
	l := launcher.New().
		Proxy("127.0.0.1:8080").    // set flag "--proxy-server=127.0.0.1:8080"
		Delete("use-mock-keychain") // delete flag "--use-mock-keychain"
	defer func() {
		l.Kill()
		l.Cleanup()
	}()
	url := l.MustLaunch()

	browser := rod.New().ControlURL(url).MustConnect()
	defer browser.MustClose()

	// So that we don't have to self issue certs for MITM
	browser.MustIgnoreCertErrors(true)

	// Adding authentication to the proxy, for the next auth request.
	// We use CLI tool "mitmproxy --proxyauth user:pass" as an example.
	go browser.MustHandleAuth("user", "pass")()

	// mitmproxy needs a cert config to support https. We use http here instead,
	// for example
	fmt.Println(browser.MustPage("https://mdn.dev/").MustElement("title").MustText())
}

// When rod doesn't have a feature that you need. You can easily call the cdp to achieve it.
// List of cdp API: https://github.com/rah-0/rod/tree/main/lib/proto
func Example_direct_cdp() {
	browser := rod.New().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage()

	// Rod doesn't have a method to enable AD blocking,
	// but you can call cdp interface directly to achieve it.

	// The two code blocks below are equal to enable AD blocking

	{
		_ = proto.PageSetAdBlockingEnabled{
			Enabled: true,
		}.Call(page)
	}

	{
		// Interact with the cdp JSON API directly
		_, _ = page.Call(context.TODO(), "", "Page.setAdBlockingEnabled", map[string]bool{
			"enabled": true,
		})
	}
}

// Shows how to listen for events.
func Example_handle_events() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Event example</title>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	browser := rod.New().Context(ctx).NoDefaultDevice().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage()

	// Listen until the first console log, then collect the listener's error.
	waitConsole := page.EachEvent(rod.On(func(e *proto.RuntimeConsoleAPICalled, _ proto.TargetSessionID) bool {
		if e.Type == proto.RuntimeConsoleAPICalledTypeLog {
			fmt.Println(page.MustObjectsToJSON(e.Args))
			return true
		}
		return false
	}))
	done := make(chan error, 1)
	go func() { done <- waitConsole() }()

	wait := page.WaitEvent(&proto.PageLoadEventFired{})
	page.MustNavigate(server.URL)
	utils.E(wait())

	// EachEvent allows us to achieve the same functionality as above.
	if false {
		// Subscribe events before they happen, run the "wait()" to start consuming
		// the events. We can return an optional stop signal to unsubscribe events.
		wait := page.EachEvent(rod.On(func(_ *proto.PageLoadEventFired, _ proto.TargetSessionID) (stop bool) {
			return true
		}))
		page.MustNavigate(server.URL)
		utils.E(wait())
	}

	// Or the for-loop style to handle events to do the same thing above.
	if false {
		// Raw event subscriptions need their protocol domain enabled explicitly.
		utils.E(proto.PageEnable{}.Call(page))
		eventPage, cancel := page.WithCancel()
		defer cancel()
		events := eventPage.Event()
		page.MustNavigate(server.URL)

		for msg := range events {
			e := proto.PageLoadEventFired{}
			if msg.Load(&e) {
				break
			}
		}
		cancel()
		utils.E(proto.PageDisable{}.Call(page))
	}

	page.MustEval(`() => console.log("hello", "world")`)

	utils.E(<-done)

	// Output:
	// [hello world]
}

func Example_download_file() {
	browser := rod.New().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage("https://file-examples.com/index.php/sample-documents-download/sample-pdf-download/")

	wait := browser.MustWaitDownload()

	page.MustElementR("a", "DOWNLOAD SAMPLE PDF FILE").MustClick()

	_ = utils.OutputFile("t.pdf", wait())
}

// Shows how to intercept requests and modify
// both the request and the response.
// The entire process of hijacking one request:
//
//	browser --req-> rod ---> server ---> rod --res-> browser
//
// The --req-> and --res-> are the parts that can be modified.
func Example_hijack_requests() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app.js" {
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = fmt.Fprint(w, `document.title = "before"`)
			return
		}
		_, _ = fmt.Fprint(w, `<script src="/app.js"></script>`)
	}))
	defer server.Close()

	browser := rod.New().MustConnect()
	defer browser.MustClose()

	router := browser.HijackRequests()

	router.MustAdd("*.js", func(ctx *rod.Hijack) {
		// Here we update the request's header. Rod gives functionality to
		// change or update all parts of the request. Refer to the documentation
		// for more information.
		ctx.Request.Req().Header.Set("My-Header", "test")

		// LoadResponse runs the default request to the destination of the request.
		// Not calling this will require you to mock the entire response.
		// This can be done with the SetXxx (Status, Header, Body) functions on the
		// ctx.Response struct.
		ctx.MustLoadResponse()

		// Here we append some code to every js file.
		// The code will update the document title to "hi"
		ctx.Response.SetBody(ctx.Response.Body() + "\n document.title = 'hi' ")
	})

	done := make(chan error, 1)
	go func() { done <- router.Run() }()
	defer func() {
		stopErr := router.Stop()
		runErr := <-done
		utils.E(errors.Join(stopErr, runErr))
	}()

	browser.MustPage(server.URL).MustWait(`() => document.title === 'hi'`)

	fmt.Println("done")

	// Output: done
}

// Shows how to share a remote object reference between two Eval.
func Example_eval_reuse_remote_object() {
	browser := rod.New().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage()

	fn := page.MustEvaluate(rod.Eval(`() => Math.random`).ByObject())

	res := page.MustEval(`f => f()`, fn)

	// print a random number
	fmt.Println(res.Num())
}

// Shows how to update the state of the current page.
// In this example we enable the network domain.
func Example_states() {
	browser := rod.New().MustConnect()
	defer browser.MustClose()

	page := browser.MustPage()

	// LoadState detects whether the network domain is enabled or not.
	fmt.Println(page.LoadState(&proto.NetworkEnable{}))

	_ = proto.NetworkEnable{}.Call(page)

	// Check if the network domain is successfully enabled.
	fmt.Println(page.LoadState(&proto.NetworkEnable{}))

	// Output:
	// false
	// true
}

// We can use [rod.PagePool] to concurrently control and reuse pages.
func ExamplePage_pool() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><title>Pooled page</title>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	browser := rod.New().Context(ctx).NoDefaultDevice().MustConnect()
	defer browser.MustClose()

	// We create a pool that will hold at most 3 pages which means the max concurrency is 3
	pool := rod.NewPagePool(3)

	// Create a page if needed
	create := func() *rod.Page {
		// We use MustIncognito to isolate pages with each other
		return browser.MustIncognito().MustPage()
	}

	yourJob := func() {
		page := pool.MustGet(create)

		// Put the instance back to the pool after we're done,
		// so the instance can be reused by other goroutines.
		defer pool.Put(page)

		page.MustNavigate(server.URL).MustWaitLoad()
		fmt.Println(page.MustInfo().Title)
	}

	// Run jobs concurrently. Explicit Done preserves cleanup if a Must helper panics.
	wg := sync.WaitGroup{}
	for range "...." {
		wg.Add(1)
		go func() {
			defer wg.Done()
			yourJob()
		}()
	}
	wg.Wait()

	// cleanup pool
	pool.Cleanup(func(p *rod.Page) { p.MustClose() })

	// Output:
	// Pooled page
	// Pooled page
	// Pooled page
	// Pooled page
}

// We can use [rod.BrowserPool] to concurrently control and reuse browsers.
func ExampleBrowser_pool() {
	// Create a new browser pool with a limit of 3
	pool := rod.NewBrowserPool(3)
	// Close every pooled browser when the example returns.
	defer pool.Cleanup(func(browser *rod.Browser) {
		browser.MustClose()
	})

	// Create a function that returns a new browser instance
	create := func() *rod.Browser {
		browser := rod.New().MustConnect()
		return browser
	}

	// Explicit Done preserves cleanup if a Must helper panics.
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Get a browser instance from the pool
			browser := pool.MustGet(create)

			// Put the instance back to the pool after we're done,
			// so the instance can be reused by other goroutines.
			defer pool.Put(browser)

			// Use the browser instance
			page := browser.MustPage("https://www.google.com")
			fmt.Println(page.MustInfo().Title)
		}()
	}

	// Wait for all the goroutines to finish
	wg.Wait()
}

func Example_log_cdp_traffic() {
	l := launcher.New()
	defer func() {
		l.Kill()
		l.Cleanup()
	}()
	u := l.MustLaunch()

	cdp := cdp.New().
		// Here we can customize how to log the requests, responses, and events transferred between Rod and the browser.
		Logger(utils.Log(func(args ...any) {
			switch v := args[0].(type) {
			case *cdp.Request:
				fmt.Printf("id: %d", v.ID)
			}
		})).
		Start(cdp.MustConnectWS(u))

	browser := rod.New().Client(cdp).MustConnect()
	defer browser.MustClose()

	browser.MustPage("http://mdn.dev")
}
