package rod

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
)

func TestExposeAuthorizesBrowserSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		var calls []ExposedCall
		stop, err := page.ExposeWithOptions("restricted", ExposeOptions{
			Frames: []proto.PageFrameID{"child"}, Origins: []string{"https://trusted.test"}, Documents: []string{"allowed-document"},
		}, func(call ExposedCall) (any, error) {
			calls = append(calls, call)
			return "accepted", nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(client.release)
			if err := stop(); err != nil {
				t.Fatal(err)
			}
		}()
		client.context(7, "child", "https://trusted.test", "allowed-document")
		client.context(8, "child", "https://untrusted.test", "other-document")
		for _, id := range []proto.RuntimeExecutionContextID{5, 8, 7} {
			client.invokeFrom(id, client.binding, client.binding+"_cb0", "request")
			reply := client.next()
			if id == 7 && !strings.HasSuffix(reply.replies[0], `="accepted"`) || id != 7 && !strings.Contains(reply.replies[0], "not authorized") {
				t.Fatalf("context %d reply: %+v", id, reply)
			}
		}
		client.context(7, "child", "https://trusted.test", "new-document")
		client.invokeFrom(7, client.binding, client.binding+"_cb1", nil)
		if reply := client.next(); !strings.Contains(reply.replies[0], "not authorized") {
			t.Fatal(reply)
		}
		if len(calls) != 1 || calls[0].Source.DocumentID != "allowed-document" || calls[0].Source.FrameID != "child" || calls[0].Source.Origin != "https://trusted.test" || calls[0].Source.TargetID != page.TargetID || calls[0].Source.SessionID != page.SessionID {
			t.Fatalf("trusted caller metadata: %+v", calls)
		}
	})
}

func TestExposeDefaultRejectsChildAndStaleQueuedCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		blocked := make(chan struct{})
		var calls []int
		stop, err := page.Expose("restricted", func(request jsonvalue.Value) (any, error) {
			calls = append(calls, request.Int())
			if request.Int() == 1 {
				<-blocked
			}
			return request.Int(), nil
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(client.release)
			if err := stop(); err != nil {
				t.Fatal(err)
			}
		}()
		client.context(7, "child", "https://trusted.test", "child-document")
		client.invokeFrom(7, client.binding, client.binding+"_cb0", 0)
		if reply := client.next(); !strings.Contains(reply.replies[0], "not authorized") {
			t.Fatal(reply)
		}
		client.invoke(client.binding, 1, 1)
		synctest.Wait()
		client.invoke(client.binding, 2, 2)
		synctest.Wait()
		client.context(5, "target", "https://trusted.test", "replacement-document")
		synctest.Wait()
		close(blocked)
		synctest.Wait()
		var replies []string
		for len(replies) < 2 {
			replies = append(replies, client.next().replies...)
		}
		if !slices.Equal(calls, []int{1}) || !strings.Contains(replies[1], "not authorized") {
			t.Fatalf("calls=%v replies=%v", calls, replies)
		}
	})
}

func TestExposeReplyLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		reported := make(chan error, 1)
		stop, err := page.ExposeWithOptions("bounded", ExposeOptions{MaxReplyBytes: 4}, func(ExposedCall) (any, error) {
			return "too large", nil
		}, func(err error) { reported <- err })
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			close(client.release)
			if err := stop(); err != nil {
				t.Fatal(err)
			}
		}()
		client.invoke(client.binding, 0, nil)
		if reply := client.next(); !strings.Contains(reply.replies[0], "exceeds byte limit") {
			t.Fatal(reply)
		}
		if err := <-reported; !errors.Is(err, ErrExposedFunctionLimit) {
			t.Fatal(err)
		}
	})
}

func TestFileDialogRejectsOtherFramesAndMissingNodes(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "expected input", true: "missing node"}[invalid], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				page, client := newExposeTestPage(t)
				var uploads []proto.DOMSetFileInputFiles
				original := client.call
				client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
					if method == "DOM.setFileInputFiles" {
						uploads = append(uploads, params.(proto.DOMSetFileInputFiles))
					}
					return original(ctx, session, method, params)
				}
				wait, err := page.HandleFileDialog()
				if err != nil {
					t.Fatal(err)
				}
				page.browser.event.Publish(eventTestMessage("Page.fileChooserOpened", page.SessionID, `{"frameId":"child","backendNodeId":12,"mode":"selectSingle"}`))
				if invalid {
					page.browser.event.Publish(eventTestMessage("Page.fileChooserOpened", page.SessionID, `{"frameId":"target","mode":"selectSingle"}`))
				} else {
					page.browser.event.Publish(eventTestMessage("Page.fileChooserOpened", page.SessionID, `{"frameId":"target","backendNodeId":42,"mode":"selectSingle"}`))
				}
				err = wait([]string{"private.txt"})
				if invalid {
					if !errors.Is(err, ErrFileDialog) || len(uploads) != 0 {
						t.Fatalf("uploads=%v error=%v", uploads, err)
					}
				} else if err != nil || len(uploads) != 1 || uploads[0].ObjectID != "file-input" || uploads[0].BackendNodeID != 0 {
					t.Fatalf("uploads=%v error=%v", uploads, err)
				}
			})
		})
	}
}

func TestPageDownloadFiltersAndBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newDownloadTestBrowser(t)
		page := &Page{FrameID: "normal", browser: browser, ctx: t.Context()}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "unwanted"), []byte("discard"), 0600); err != nil {
			t.Fatal(err)
		}
		var canceled []string
		original := client.call
		client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
			if method == "Browser.cancelDownload" {
				canceled = append(canceled, params.(proto.BrowserCancelDownload).GUID)
			}
			return original(ctx, session, method, params)
		}
		wait, err := page.WaitDownload(dir, DownloadOptions{URL: "https://trusted.test/report", MaxBytes: 8})
		if err != nil {
			t.Fatal(err)
		}
		browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"child","guid":"unwanted","url":"https://trusted.test/report","suggestedFilename":"report"}`))
		browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"wanted","url":"https://trusted.test/report","suggestedFilename":"report"}`))
		browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"wanted","state":"inProgress","totalBytes":9,"receivedBytes":9}`))
		info, err := wait()
		if !errors.Is(err, ErrDownloadTooLarge) || info == nil || info.GUID != "wanted" || !slices.Equal(canceled, []string{"unwanted", "wanted"}) {
			t.Fatalf("info=%+v err=%v canceled=%v", info, err, canceled)
		}
		// The directory belongs to the browser host, which may be remote. A
		// canceled remote GUID must never remove a same-named client file.
		if data, err := os.ReadFile(filepath.Join(dir, "unwanted")); err != nil || string(data) != "discard" {
			t.Fatalf("client file changed: %q, %v", data, err)
		}
	})
}

func TestDownloadTimeoutWithoutInvokingWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newDownloadTestBrowser(t)
		wait, err := browser.WaitDownload(t.TempDir(), DownloadOptions{Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if _, active := browser.states.Load(downloadWaitKey("")); active {
			t.Fatal("unused timed out wait retains download behavior")
		}
		if _, err := wait(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
}

func TestExposeOverflowReportsOnceWhenErrorHandlerExits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		blocked := make(chan struct{})
		var calls, reports int
		stop, err := page.EventBuffer(1, 1024).Expose("bounded", func(jsonvalue.Value) (any, error) {
			calls++
			<-blocked
			return nil, nil
		}, func(err error) {
			if !errors.Is(err, ErrEventOverflow) {
				t.Errorf("overflow error=%v", err)
			}
			reports++
			runtime.Goexit()
		})
		if err != nil {
			t.Fatal(err)
		}
		client.invoke(client.binding, 0, nil)
		synctest.Wait()
		for i := 1; i < 4; i++ {
			client.invoke(client.binding, i, nil)
		}
		synctest.Wait()
		close(blocked)
		close(client.release)
		synctest.Wait()
		if err := stop(); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || reports != 1 {
			t.Fatalf("calls=%d reports=%d", calls, reports)
		}
	})
}

func TestFileDialogRejectsNavigationAfterChooser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newExposeTestPage(t)
		navigated, uploaded := false, false
		original := client.call
		client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
			if method == "Page.getFrameTree" && navigated {
				return []byte(`{"frameTree":{"frame":{"id":"target","loaderId":"new-document","url":"","securityOrigin":"","mimeType":""}}}`), nil
			}
			if method == "DOM.setFileInputFiles" {
				uploaded = true
			}
			return original(ctx, session, method, params)
		}
		wait, err := page.HandleFileDialog()
		if err != nil {
			t.Fatal(err)
		}
		page.browser.event.Publish(eventTestMessage("Page.fileChooserOpened", page.SessionID, `{"frameId":"target","backendNodeId":42,"mode":"selectSingle"}`))
		navigated = true
		if err := wait([]string{"private.txt"}); !errors.Is(err, ErrFrameContextChanged) || uploaded {
			t.Fatalf("error=%v uploaded=%t", err, uploaded)
		}
	})
}

func TestDownloadOverflowCannotReturnSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newDownloadTestBrowser(t)
		wait, err := browser.EventBuffer(1, 4096).WaitDownload(t.TempDir(), DownloadOptions{Match: func(proto.BrowserDownloadWillBegin) bool {
			for range 4 {
				browser.event.Publish(eventTestMessage("Browser.downloadProgress", "", `{"guid":"guid","state":"completed","totalBytes":0,"receivedBytes":0}`))
			}
			return true
		}})
		if err != nil {
			t.Fatal(err)
		}
		browser.event.Publish(eventTestMessage("Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"guid","url":"","suggestedFilename":""}`))
		if _, err := wait(); !errors.Is(err, ErrEventOverflow) {
			t.Fatal(err)
		}
	})
}
