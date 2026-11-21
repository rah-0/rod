package rod_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/jsonvalue"
)

func TestExposeRejectsSameProcessChild(t *testing.T) {
	for _, crossOrigin := range []bool{false, true} {
		t.Run(fmt.Sprint("cross origin ", crossOrigin), func(t *testing.T) {
			g := setup(t)
			markup := `<!doctype html><iframe srcdoc="<p>child</p>"></iframe>`
			if crossOrigin {
				childServer := g.Serve().Route("/child", ".html", "<p>child</p>")
				markup = fmt.Sprintf(`<iframe src="%s"></iframe>`, childServer.URL("/child"))
			}
			page := g.newPage(g.html(markup)).Timeout(10 * time.Second)
			defer page.CancelTimeout()
			var calls atomic.Int32
			stop := page.MustExpose("restricted", func(_ jsonvalue.Value) (any, error) {
				calls.Add(1)
				return "private", nil
			}, nil)
			defer stop()
			page.MustReload().MustWaitLoad()
			child := page.MustElement("iframe").MustFrame()
			if child.SessionID != page.SessionID {
				t.Fatal("fixture must exercise shared-session authorization")
			}
			result := child.MustEval(`async () => {
				try { return await restricted(null) } catch (error) { return String(error) }
			}`)
			if result.Str() != "exposed function caller is not authorized" || calls.Load() != 0 {
				t.Fatalf("child received %v; calls=%d", result, calls.Load())
			}
			if result := page.MustEval(`() => restricted(null)`); result.Str() != "private" || calls.Load() != 1 {
				t.Fatalf("top frame received %v; calls=%d", result, calls.Load())
			}
		})
	}
}

func TestFileDialogChildCannotReceiveTopFrameFiles(t *testing.T) {
	for _, crossOrigin := range []bool{false, true} {
		t.Run(fmt.Sprint("cross origin ", crossOrigin), func(t *testing.T) {
			g := setup(t)
			markup := `<!doctype html><input type="file"><iframe srcdoc="<input type='file'>"></iframe>`
			if crossOrigin {
				childServer := g.Serve().Route("/child", ".html", `<input type="file">`)
				markup = fmt.Sprintf(`<!doctype html><input type="file"><iframe src="%s"></iframe>`, childServer.URL("/child"))
			}
			page := g.newPage(g.html(markup)).Timeout(10 * time.Second)
			defer page.CancelTimeout()
			page.MustWaitLoad()
			child := page.MustElement("iframe").MustFrame()
			if child.SessionID != page.SessionID {
				t.Fatal("fixture must exercise shared-session authorization")
			}
			file := filepath.Join(t.TempDir(), "private.txt")
			if err := os.WriteFile(file, []byte("private content"), 0600); err != nil {
				t.Fatal(err)
			}
			setFiles := page.MustHandleFileDialog()
			child.MustElement("input").MustClick()
			page.MustElement("input").MustClick()
			setFiles(file)
			if count := child.MustElement("input").MustEval(`() => this.files.length`).Int(); count != 0 {
				t.Fatalf("child received %d files", count)
			}
			if count := page.MustElement("input").MustEval(`() => this.files.length`).Int(); count != 1 {
				t.Fatalf("top frame received %d files", count)
			}
		})
	}
}

func TestPageDownloadRejectsAnotherPage(t *testing.T) {
	g := setup(t)
	server := g.Serve()
	server.Route("/wanted", ".bin", "expected")
	server.Route("/other", ".bin", "unwanted")
	server.Route("/page", ".html", `<a href="/wanted" download="report.bin">download</a>`)
	server.Route("/attacker", ".html", `<a href="/other" download="report.bin">download</a>`)
	page := g.newPage(server.URL("/page")).Timeout(10 * time.Second)
	defer page.CancelTimeout()
	other := g.newPage(server.URL("/attacker"))
	wait := page.MustWaitDownload()
	other.MustElement("a").MustClick()
	page.MustActivate().MustElement("a").MustClick()
	info, data := wait()
	if info.FrameID != page.FrameID || string(data) != "expected" {
		t.Fatalf("download=%+v bytes=%q", info, data)
	}
}

func TestDownloadConvenienceLimitsRead(t *testing.T) {
	browser, client, dirs := newMustDownloadTestBrowser(t, false)
	wait := browser.MustWaitDownloadWithInfo(rod.DownloadOptions{MaxBytes: 4})
	dir := <-dirs
	if err := os.WriteFile(filepath.Join(dir, "guid"), []byte("larger than reported"), 0600); err != nil {
		t.Fatal(err)
	}
	sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"guid","url":"https://trusted.test/report","suggestedFilename":"report"}`)
	sendEvent(client.events, "Browser.downloadProgress", "", `{"guid":"guid","state":"completed","totalBytes":4,"receivedBytes":4}`)
	func() {
		defer func() {
			failure, _ := recover().(error)
			if !errors.Is(failure, rod.ErrDownloadTooLarge) {
				t.Fatalf("bounded read returned %v", failure)
			}
		}()
		wait()
	}()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed read left directory: %v", err)
	}
}

func TestDownloadConvenienceUnusedTimeoutCleansDirectory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _, dirs := newMustDownloadTestBrowser(t, false)
		_ = browser.MustWaitDownloadWithInfo(rod.DownloadOptions{Timeout: time.Second})
		dir := <-dirs
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("timed out unused wait left directory: %v", err)
		}
	})
}
