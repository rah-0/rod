// This is the setup file for this test suite.

package main

import (
	"encoding/base64"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
)

// test context.
type G struct {
	testutil.G

	browser *rod.Browser
}

// setup for tests.
var setup = func() func(t *testing.T) G {
	browser := rod.New().MustConnect()

	return func(t *testing.T) G {
		t.Parallel() // run each test concurrently

		return G{testutil.New(t), browser}
	}
}()

// a helper function to create an incognito page.
func (g G) page(html string) *rod.Page {
	url := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html))
	page := g.browser.MustIncognito().MustPage(url)
	g.Cleanup(page.MustClose)
	return page
}
