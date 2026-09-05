// This is the setup file for this test suite.

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/defaults"
)

// G provides assertions and a browser for a test.
type G struct {
	testutil.G
	browser *rod.Browser
}

var (
	browserOnce  sync.Once
	suiteBrowser *rod.Browser
)

func TestMain(m *testing.M) {
	defaults.Load()
	code := m.Run()
	if suiteBrowser != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := suiteBrowser.Context(ctx).Close()
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "close suite browser:", err)
			code = 1
		}
	}
	os.Exit(code)
}

func setup(t *testing.T) G {
	t.Helper()
	t.Parallel()
	// Connect only when a selected test needs the browser.
	browserOnce.Do(func() { suiteBrowser = rod.New().MustConnect() })
	return G{testutil.New(t), suiteBrowser.Context(t.Context())}
}

// page creates an isolated browser context and closes it after the test.
func (g G) page(html string) *rod.Page {
	g.Helper()
	browser := g.browser.MustIncognito()
	g.Cleanup(func() {
		// Native test contexts are canceled before cleanup begins.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := browser.Context(ctx).Close(); err != nil {
			g.Errorf("close incognito browser: %v", err)
		}
	})
	url := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html))
	return browser.MustPage(url)
}
