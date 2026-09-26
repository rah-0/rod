package rod_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

// newDownloadTestBrowser returns a connected browser whose client reports a page
// in the default browser context and one in the "incognito" context.
func newDownloadTestBrowser(t *testing.T) (*rod.Browser, *sessionTestClient) {
	t.Helper()
	client := &sessionTestClient{events: make(chan *cdp.Event), call: func(_ context.Context, _, method string, _ any) ([]byte, error) {
		switch method {
		case "Target.getTargets":
			return []byte(`{"targetInfos":[{"targetId":"normal","type":"page","browserContextId":"default","title":"","url":"","attached":false},{"targetId":"private","type":"page","browserContextId":"incognito","title":"","url":"","attached":false}]}`), nil
		case "Target.getBrowserContexts":
			return []byte(`{"browserContextIds":["incognito"]}`), nil
		case "Target.attachToTarget":
			return []byte(`{"sessionId":"temporary"}`), nil
		case "Page.getFrameTree":
			return []byte(`{"frameTree":{"frame":{"id":"normal","loaderId":"","url":"","securityOrigin":"","mimeType":""},"childFrames":[{"frame":{"id":"child","loaderId":"","url":"","securityOrigin":"","mimeType":""}}]}}`), nil
		default:
			return []byte(`{}`), nil
		}
	}}
	return connectTestBrowser(t, rod.New().Context(t.Context()).Client(client)), client
}

func TestDownloadBrowserCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newDownloadTestBrowser(t)
		wait, err := browser.WaitDownload("/downloads")
		if err != nil {
			t.Fatal(err)
		}
		sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"canceled","url":"","suggestedFilename":""}`)
		sendEvent(client.events, "Browser.downloadProgress", "", `{"guid":"canceled","state":"canceled","totalBytes":0,"receivedBytes":0}`)
		info, err := wait()
		if info == nil || info.GUID != "canceled" || !errors.Is(err, rod.ErrDownloadCanceled) {
			t.Fatalf("browser canceled: %+v, %v", info, err)
		}
	})
}

func TestDownloadDistinctContexts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newDownloadTestBrowser(t)
		private := browser.Context(t.Context())
		private.BrowserContextID = "incognito"
		waitRoot, err := browser.WaitDownload("/normal")
		if err != nil {
			t.Fatal(err)
		}
		waitPrivate, err := private.WaitDownload("/private")
		if err != nil {
			t.Fatal(err)
		}
		sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"normal","url":"","suggestedFilename":""}`)
		sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"private","guid":"private","url":"","suggestedFilename":""}`)
		sendEvent(client.events, "Browser.downloadProgress", "", `{"guid":"private","state":"completed","totalBytes":0,"receivedBytes":0}`)
		sendEvent(client.events, "Browser.downloadProgress", "", `{"guid":"normal","state":"completed","totalBytes":0,"receivedBytes":0}`)
		one, err := waitRoot()
		if err != nil || one.GUID != "normal" {
			t.Fatalf("default: %+v, %v", one, err)
		}
		two, err := waitPrivate()
		if err != nil || two.GUID != "private" {
			t.Fatalf("incognito: %+v, %v", two, err)
		}
	})
}

func TestDownloadDisposedContextIsNotDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newDownloadTestBrowser(t)
		wait, err := browser.WaitDownload("/normal")
		if err != nil {
			t.Fatal(err)
		}
		call := client.call
		client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
			if method == "Target.getBrowserContexts" {
				return []byte(`{"browserContextIds":[]}`), nil
			}
			return call(ctx, session, method, params)
		}
		sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"private","guid":"closed-context","url":"","suggestedFilename":""}`)
		sendEvent(client.events, "Browser.downloadProgress", "", `{"guid":"closed-context","state":"completed","totalBytes":0,"receivedBytes":0}`)
		sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"normal","url":"","suggestedFilename":""}`)
		sendEvent(client.events, "Browser.downloadProgress", "", `{"guid":"normal","state":"completed","totalBytes":0,"receivedBytes":0}`)
		info, err := wait()
		if err != nil || info.GUID != "normal" {
			t.Fatalf("download after context disposal: %+v, %v", info, err)
		}
	})
}

// The GUID names the saved file, so a browser endpoint must not be able to
// point it outside the download directory.
func TestDownloadRejectsUnsafeGUID(t *testing.T) {
	for _, guid := range []string{"", ".", "..", "../outside", "../../home/user/.ssh/id_ed25519", "nested/file", `nested\file`, "/absolute", "C:relative", "stream:name", "nul\x00byte"} {
		t.Run(guid, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser, client := newDownloadTestBrowser(t)
				wait, err := browser.WaitDownload("/downloads")
				if err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(guid)
				// An unsafe GUID in another browser context is not this wait's download.
				sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"private","guid":"../other-context","url":"","suggestedFilename":""}`)
				sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"normal","guid":`+string(encoded)+`,"url":"","suggestedFilename":""}`)
				sendEvent(client.events, "Browser.downloadProgress", "", `{"guid":`+string(encoded)+`,"state":"completed","totalBytes":0,"receivedBytes":0}`)
				info, err := wait()
				if info != nil || !errors.Is(err, rod.ErrDownloadGUID) {
					t.Fatalf("unsafe GUID %q: %+v, %v", guid, info, err)
				}
				var restored proto.BrowserSetDownloadBehavior
				if !browser.LoadState("", &restored) || restored.Behavior != proto.BrowserSetDownloadBehaviorBehaviorDefault {
					t.Fatalf("rejected download did not restore behavior: %+v", restored)
				}
			})
		})
	}
}

// Null target records are malformed protocol data. They fail the setup or end
// the wait with proto.ErrMissingField instead of being dereferenced.
func TestDownloadNullTargets(t *testing.T) {
	t.Run("target list", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			browser, client := newDownloadTestBrowser(t)
			call := client.call
			client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
				if method == "Target.getTargets" {
					return []byte(`{"targetInfos":[null,{"targetId":"normal","type":"page","browserContextId":"default","title":"","url":"","attached":false}]}`), nil
				}
				return call(ctx, session, method, params)
			}
			wait, err := browser.WaitDownload("/downloads")
			if wait != nil || !errors.Is(err, proto.ErrMissingField) {
				t.Fatalf("setup with a null target = %v", err)
			}
			if browser.LoadState("", &proto.BrowserSetDownloadBehavior{}) {
				t.Fatal("failed setup changed the download behavior")
			}
		})
	})
	for _, event := range []struct{ method, data string }{
		{"Target.targetCreated", `{"targetInfo":null}`},
		{"Target.targetInfoChanged", `{}`},
	} {
		t.Run(event.method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser, client := newDownloadTestBrowser(t)
				wait, err := browser.WaitDownload("/downloads")
				if err != nil {
					t.Fatal(err)
				}
				sendEvent(client.events, event.method, "", event.data)
				sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"guid","url":"","suggestedFilename":""}`)
				if info, err := wait(); info != nil || !errors.Is(err, proto.ErrMissingField) {
					t.Fatalf("wait after a null target = %+v, %v", info, err)
				}
			})
		})
	}
}

// newMustDownloadTestBrowser reports the directory of each download wait. With
// failSetup, configuring that directory fails.
func newMustDownloadTestBrowser(t *testing.T, failSetup bool) (*rod.Browser, *sessionTestClient, chan string) {
	t.Helper()
	browser, client := newDownloadTestBrowser(t)
	dirs := make(chan string, 1)
	call := client.call
	client.call = func(ctx context.Context, session, method string, params any) ([]byte, error) {
		if request, ok := params.(proto.BrowserSetDownloadBehavior); ok && request.DownloadPath != "" {
			dirs <- request.DownloadPath
			if failSetup {
				return nil, context.Canceled
			}
		}
		return call(ctx, session, method, params)
	}
	return browser, client, dirs
}

func TestMustWaitDownloadPrivateDirectory(t *testing.T) {
	browser, client, dirs := newMustDownloadTestBrowser(t, false)
	wait := browser.MustWaitDownload()
	dir := <-dirs
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("download directory %q: %v, %v", dir, info, err)
	}
	if dir == filepath.Join(os.TempDir(), "rod", "downloads") {
		t.Fatal("download directory is shared and predictable")
	}
	if err := os.WriteFile(filepath.Join(dir, "guid"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"guid","url":"","suggestedFilename":""}`)
	sendEvent(client.events, "Browser.downloadProgress", "", `{"guid":"guid","state":"completed","totalBytes":0,"receivedBytes":0}`)
	if data := wait(); string(data) != "data" {
		t.Fatalf("downloaded bytes = %q", data)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("download directory remains after reading: %v", err)
	}

	another := browser.MustWaitDownload()
	if next := <-dirs; next == dir {
		t.Fatal("download directory was reused")
	}
	sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"missing","url":"","suggestedFilename":""}`)
	sendEvent(client.events, "Browser.downloadProgress", "", `{"guid":"missing","state":"completed","totalBytes":0,"receivedBytes":0}`)
	func() {
		defer func() {
			if recovered := recover(); !errors.Is(recovered.(error), os.ErrNotExist) {
				t.Fatalf("missing download panic = %v", recovered)
			}
		}()
		another()
	}()
}

func TestMustWaitDownloadFailureRemovesDirectory(t *testing.T) {
	for _, failure := range []string{"setup", "canceled", "unsafe-guid"} {
		t.Run(failure, func(t *testing.T) {
			browser, client, dirs := newMustDownloadTestBrowser(t, failure == "setup")
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			var want error = rod.ErrDownloadCanceled
			wait := func() []byte { return browser.MustWaitDownload()() }
			if failure == "setup" {
				want = context.Canceled
			} else {
				pending := browser.MustWaitDownload()
				dir := <-dirs
				guid := "guid"
				if failure == "unsafe-guid" {
					want = rod.ErrDownloadGUID
					relative, err := filepath.Rel(dir, victim)
					if err != nil {
						t.Fatal(err)
					}
					encoded, _ := json.Marshal(relative)
					guid = string(encoded[1 : len(encoded)-1])
				}
				sendEvent(client.events, "Browser.downloadWillBegin", "", `{"frameId":"normal","guid":"`+guid+`","url":"","suggestedFilename":""}`)
				sendEvent(client.events, "Browser.downloadProgress", "", `{"guid":"`+guid+`","state":"canceled","totalBytes":0,"receivedBytes":0}`)
				wait = pending
				dirs <- dir
			}
			func() {
				defer func() {
					recovered, _ := recover().(error)
					if !errors.Is(recovered, want) {
						t.Fatalf("panic = %v, want %v", recovered, want)
					}
				}()
				wait()
			}()
			dir := <-dirs
			if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("download directory remains after failure: %v", err)
			}
			if data, err := os.ReadFile(victim); err != nil || string(data) != "secret" {
				t.Fatalf("file outside the download directory changed: %q, %v", data, err)
			}
		})
	}
}
