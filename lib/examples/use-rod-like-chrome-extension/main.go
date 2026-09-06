// Package main ...
package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// For example, when you log into your github account, and you want to reuse the login session for automation task.
// You can use this example to achieve such functionality. Rod will be just like your browser extension.
func main() {
	// Make sure you have closed your browser completely, UserMode can't control a browser that is not launched by it.
	// Launches a new browser with the "new user mode" option, and returns the URL to control that browser.
	l := launcher.NewUserMode()
	defer func() {
		l.Kill()
		l.Cleanup()
	}()
	wsURL := l.MustLaunch()

	browser := rod.New().ControlURL(wsURL).MustConnect().NoDefaultDevice()
	defer browser.MustClose()

	previewer := rod.New().MustConnect()
	defer previewer.MustClose()

	// Run a extension. Here we created a link previewer extension as an example.
	// With this extension, whenever you hover on a link a preview of the linked page will popup.
	linkPreviewer(browser, previewer)

	browser.MustPage()

	waitExit()
}

func linkPreviewer(browser, previewer *rod.Browser) {
	// Create a headless browser to generate preview of links on background.
	previewer.MustSetCookies(browser.MustGetCookies()...) // share cookies
	pool := rod.NewPagePool(5)
	create := func() *rod.Page { return previewer.MustPage() }

	go browser.EachEvent(rod.On(func(e *proto.TargetTargetCreated, _ proto.TargetSessionID) bool {
		if e.TargetInfo.Type != proto.TargetTargetInfoTypePage {
			return false
		}
		page := browser.MustPageFromTargetID(e.TargetInfo.TargetID)

		// Inject js to every new page
		page.MustEvalOnNewDocument(js)

		// Expose a function to the page to provide preview
		page.MustExpose("getPreview", func(url jsonvalue.Value) (any, error) {
			p := pool.MustGet(create)
			defer pool.Put(p)
			p.MustNavigate(url.Str())
			return base64.StdEncoding.EncodeToString(p.MustScreenshot()), nil
		})
		return false
	}))()
}

var jsLib = get("https://unpkg.com/@popperjs/core@2") + get("https://unpkg.com/tippy.js@6")

var js = fmt.Sprintf(`window.addEventListener('load', () => {
	%s

	function setup(el) {
		el.classList.add('x-set')
		tippy(el, {onShow: async (it) => {
			if (it.props.content.src) return
			let img = document.createElement('img')
			img.style.width = '400px'
			img.src = "data:image/png;base64," + await getPreview(el.href)
			it.setContent(img)
		}, content: 'loading...', maxWidth: 500})
	}

	(function check() {
		Array.from(document.querySelectorAll('a:not(.x-set)')).forEach(setup)
		setTimeout(check, 1000)
	})()
})`, jsLib)

func get(u string) string {
	res, err := http.Get(u)
	utils.E(err)
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	utils.E(err)
	return string(b)
}

func waitExit() {
	fmt.Println("Press Enter to exit...")
	utils.E(fmt.Scanln())
}
