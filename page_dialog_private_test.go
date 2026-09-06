package rod

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod/internal/observable"
	"github.com/rah-0/rod/lib/proto"
)

func TestPageReloadWaitErrors(t *testing.T) {
	for _, phase := range []string{"setup", "cancellation"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				setupErr := errors.New("Page.enable failed")
				browser := New().Context(ctx).Client(&hijackTestClient{call: func(_ context.Context, method string, _ any) ([]byte, error) {
					if phase == "setup" && method == "Page.enable" {
						return nil, setupErr
					}
					if method == "Runtime.callFunctionOn" {
						if phase == "cancellation" {
							cancel()
						}
						return []byte(`{"result":{}}`), nil
					}
					return []byte(`{}`), nil
				}})
				browser.event = observable.New[*Message](ctx)
				page := &Page{browser: browser, ctx: ctx, SessionID: "session", FrameID: "frame", jsCtxLock: new(sync.Mutex), jsCtxID: new(proto.RuntimeRemoteObjectID("window")), helpers: &jsHelperCache{}}
				want := setupErr
				if phase == "cancellation" {
					want = context.Canceled
				}
				if err := page.Reload(); !errors.Is(err, want) {
					t.Fatalf("Reload = %v, want %v", err, want)
				}
			})
		})
	}
}

func TestPageHandleDialogWaitErrors(t *testing.T) {
	for _, phase := range []string{"setup", "cancellation", "success", "must"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				setupErr := errors.New("Page.enable failed")
				var enabled atomic.Int32
				var disabled atomic.Int32
				browser := New().Context(ctx).Client(&hijackTestClient{call: func(_ context.Context, method string, _ any) ([]byte, error) {
					if method == "Page.enable" {
						enabled.Add(1)
						if phase == "setup" || phase == "must" {
							return nil, setupErr
						}
					}
					if method == "Page.disable" {
						disabled.Add(1)
					}
					if method == "Page.handleJavaScriptDialog" && disabled.Load() != 1 {
						t.Error("dialog handling retained a redundant Page lease")
					}
					return []byte(`{}`), nil
				}})
				browser.event = observable.New[*Message](ctx)
				page := (&Page{browser: browser, ctx: ctx, SessionID: "session"}).WithPanic(func(value any) { panic(value) })
				if phase == "must" {
					wait, _ := page.MustHandleDialog()
					defer func() {
						err, ok := recover().(error)
						if !ok || !errors.Is(err, setupErr) {
							t.Fatalf("Must wait panic = %v", err)
						}
					}()
					wait()
					return
				}
				wait, handle := page.HandleDialog()
				if phase == "cancellation" {
					cancel()
				}
				if phase == "success" {
					browser.event.Publish(&Message{SessionID: "session", Method: "Page.javascriptDialogOpening", data: []byte(`{"message":"hello"}`)})
				}
				event, err := wait()
				switch phase {
				case "setup":
					if event != nil || !errors.Is(err, setupErr) {
						t.Fatalf("wait = %+v, %v", event, err)
					}
				case "cancellation":
					if event != nil || !errors.Is(err, context.Canceled) {
						t.Fatalf("wait = %+v, %v", event, err)
					}
				case "success":
					if err != nil || event == nil || event.Message != "hello" {
						t.Fatalf("wait = %+v, %v", event, err)
					}
					if err := handle(&proto.PageHandleJavaScriptDialog{Accept: true}); err != nil {
						t.Fatal(err)
					}
				}
				if enabled.Load() != 1 || disabled.Load() != 1 {
					t.Fatalf("Page transitions: %d enables, %d disables", enabled.Load(), disabled.Load())
				}
			})
		})
	}
}

func TestPageHandleFileDialogLifecycle(t *testing.T) {
	for _, phase := range []string{"interception setup", "event setup", "cancellation", "unused cancellation", "restore", "restore timeout", "set files", "success", "already enabled"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				sentinel := errors.New("protocol failed")
				var restores, uploads atomic.Int32
				browser := New().Context(ctx).Client(&hijackTestClient{call: func(callCtx context.Context, method string, params any) ([]byte, error) {
					switch method {
					case "Page.setInterceptFileChooserDialog":
						request := params.(proto.PageSetInterceptFileChooserDialog)
						if request.Enabled {
							if phase == "interception setup" {
								return nil, sentinel
							}
						} else {
							restores.Add(1)
							if callCtx.Err() != nil {
								t.Error("restore reused an expired context")
							}
							if _, bounded := callCtx.Deadline(); !bounded {
								t.Error("restore has no deadline")
							}
							if phase == "restore" {
								return nil, sentinel
							}
							if phase == "restore timeout" {
								<-callCtx.Done()
								return nil, callCtx.Err()
							}
						}
					case "Page.enable":
						if phase == "event setup" {
							return nil, sentinel
						}
					case "DOM.setFileInputFiles":
						uploads.Add(1)
						if params.(proto.DOMSetFileInputFiles).BackendNodeID != 42 {
							t.Error("upload used an unconfirmed chooser event")
						}
						if phase == "set files" {
							return nil, sentinel
						}
					}
					return []byte(`{}`), nil
				}})
				browser.event = observable.New[*Message](ctx)
				page := &Page{browser: browser, ctx: ctx, SessionID: "session"}
				if phase == "already enabled" {
					browser.set("session", "Page.setInterceptFileChooserDialog", proto.PageSetInterceptFileChooserDialog{Enabled: true})
				}
				setFiles, err := page.HandleFileDialog()
				if phase == "interception setup" {
					if setFiles != nil || !errors.Is(err, sentinel) || restores.Load() != 1 {
						t.Fatalf("setup = %v, restores = %d", err, restores.Load())
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if phase == "cancellation" || phase == "unused cancellation" {
					cancel()
					synctest.Wait()
				}
				if phase == "unused cancellation" {
					if restores.Load() != 1 || uploads.Load() != 0 {
						t.Fatalf("unused wait: restores=%d uploads=%d", restores.Load(), uploads.Load())
					}
					return
				}
				if phase != "event setup" && phase != "cancellation" {
					browser.event.Publish(&Message{SessionID: "session", Method: "Page.fileChooserOpened", data: []byte(`{"backendNodeId":42}`)})
				}
				err = setFiles(nil)
				switch phase {
				case "event setup", "restore", "set files":
					if !errors.Is(err, sentinel) {
						t.Fatal(err)
					}
				case "cancellation":
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case "restore timeout":
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal(err)
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
				}
				wantUploads := int32(1)
				if phase == "event setup" || phase == "cancellation" {
					wantUploads = 0
				}
				if uploads.Load() != wantUploads {
					t.Fatalf("uploads = %d, want %d", uploads.Load(), wantUploads)
				}
				wantRestores := int32(1)
				if phase == "already enabled" {
					wantRestores = 0
				}
				if restores.Load() != wantRestores {
					t.Fatalf("restores = %d, want %d", restores.Load(), wantRestores)
				}
				var state proto.PageSetInterceptFileChooserDialog
				if phase == "already enabled" && (!page.LoadState(&state) || !state.Enabled) {
					t.Fatal("existing interception was disabled")
				}
			})
		})
	}
}
