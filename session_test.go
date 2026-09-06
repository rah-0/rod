package rod_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

func TestPageCachedViewsUseCallerContext(t *testing.T) {
	g := setup(t)
	ctx, cancel := context.WithCancel(g.browser.GetContext())
	first, err := g.browser.Context(ctx).Page(proto.TargetCreateTarget{})
	g.E(err)
	defer func() { g.E(first.Context(g.browser.GetContext()).Close()) }()
	cancel()
	second, err := g.browser.PageFromTarget(first.TargetID)
	g.E(err)
	g.Eq(second.MustEval(`() => 42`).Int(), 42)
	_, err = first.Eval(`() => 1`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller cancellation: %v", err)
	}
	if first == second || first.SessionID != second.SessionID {
		t.Fatal("page views did not share one attachment")
	}
}

func TestIncognitoPagesStayInContext(t *testing.T) {
	g := setup(t)
	one, two := g.browser.MustIncognito(), g.browser.MustIncognito()
	defer one.MustClose()
	defer two.MustClose()
	first, second := one.MustPage(), two.MustPage()
	pages, err := one.Pages()
	g.E(err)
	if len(pages) != 1 || pages[0].TargetID != first.TargetID {
		t.Fatalf("first context pages: %v", pages)
	}
	pages, err = two.Pages()
	g.E(err)
	if len(pages) != 1 || pages[0].TargetID != second.TargetID {
		t.Fatalf("second context pages: %v", pages)
	}
}

func TestDownloadIframeAndIncognitoBrowser(t *testing.T) {
	g := setup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<iframe src="/frame"></iframe>`))
		case "/frame":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<a href="/file" download="report.txt">download</a>`))
		case "/file":
			w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
			_, _ = w.Write([]byte("download contents"))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	private := g.browser.MustIncognito()
	defer private.MustClose()
	page := private.MustPage(server.URL).MustWaitLoad()
	frame := page.MustElement("iframe").MustFrame()
	dir := t.TempDir()
	otherDir := t.TempDir()
	// A default-context download lets its simultaneous waiter complete after
	// the incognito iframe event has been delivered to both subscriptions.
	rootPage := g.newPage(server.URL + "/frame").MustWaitLoad()
	waitCtx, cancel := context.WithTimeout(g.browser.GetContext(), 10*time.Second)
	defer cancel()
	rootWait, err := g.browser.Context(waitCtx).WaitDownload(otherDir)
	g.E(err)
	wait, err := private.Context(waitCtx).WaitDownload(dir)
	g.E(err)
	frame.MustElement("a").MustClick()
	rootPage.MustElement("a").MustClick()
	info, err := wait()
	if err != nil {
		cancel()
		rootInfo, rootErr := rootWait()
		t.Fatalf("incognito download: %+v, %v; default download: %+v, %v", info, err, rootInfo, rootErr)
	}
	rootInfo, err := rootWait()
	g.E(err)
	if info.GUID == rootInfo.GUID {
		t.Fatal("different contexts selected the same download")
	}
	data, err := os.ReadFile(filepath.Join(dir, info.GUID))
	g.E(err)
	if string(data) != "download contents" {
		t.Fatalf("download contents: %q", data)
	}
	data, err = os.ReadFile(filepath.Join(otherDir, rootInfo.GUID))
	g.E(err)
	if string(data) != "download contents" {
		t.Fatalf("default-context download contents: %q", data)
	}
}
