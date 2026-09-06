package rod

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

func TestDomainSetupAndRestoreErrors(t *testing.T) {
	for _, operation := range []string{"enable", "disable"} {
		for _, phase := range []string{"setup", "setup-and-rollback", "restore"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				browser, _ := newEventTestBrowser(t)
				failures := make(map[string]error)
				client := &sessionTestClient{call: func(_ context.Context, _, method string, _ any) ([]byte, error) {
					return []byte(`{}`), failures[method]
				}}
				browser.Client(client)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				page := &Page{browser: browser, ctx: ctx, SessionID: "session"}
				change := page.EnableDomain
				setupMethod, restoreMethod := "Network.enable", "Network.disable"
				if operation == "disable" {
					if err := (proto.NetworkEnable{}).Call(page); err != nil {
						t.Fatal(err)
					}
					change = page.DisableDomain
					setupMethod, restoreMethod = restoreMethod, setupMethod
				}
				setupErr, restoreErr := errors.New("setup failed"), errors.New("restoration failed")
				if phase != "restore" {
					failures[setupMethod] = setupErr
					if phase == "setup-and-rollback" {
						failures[restoreMethod] = restoreErr
					}
				}
				restore, err := change(&proto.NetworkEnable{})
				if phase != "restore" {
					if restore != nil || !errors.Is(err, setupErr) {
						t.Fatalf("setup result: restore=%t, err=%v", restore != nil, err)
					}
					if phase == "setup-and-rollback" && !errors.Is(err, restoreErr) {
						t.Fatalf("rollback error was lost: %v", err)
					}
					calls := client.snapshot()
					if calls[len(calls)-1].method != restoreMethod {
						t.Fatalf("uncertain setup did not attempt rollback: %v", calls)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				failures[restoreMethod] = restoreErr
				cancel()
				if err := restore(); !errors.Is(err, restoreErr) {
					t.Fatalf("restore after caller cancellation: %v", err)
				}
				calls := len(client.snapshot())
				if err := restore(); !errors.Is(err, restoreErr) || len(client.snapshot()) != calls {
					t.Fatalf("repeated restore was not idempotent: %v", err)
				}
			})
		}
	}
}

func TestDomainDisableRestoresConfiguration(t *testing.T) {
	browser, client := newEventTestBrowser(t)
	previous := proto.FetchEnable{
		Patterns:           []*proto.FetchRequestPattern{{URLPattern: "https://example.test/*"}},
		HandleAuthRequests: new(true),
	}
	if err := previous.Call(browser); err != nil {
		t.Fatal(err)
	}
	restore, err := browser.DisableDomain("", &proto.FetchEnable{})
	if err != nil {
		t.Fatal(err)
	}
	var current proto.FetchEnable
	if browser.LoadState("", &current) {
		t.Fatal("disabled domain remained enabled")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if !browser.LoadState("", &current) || !reflect.DeepEqual(previous, current) {
		t.Fatalf("previous configuration was not restored: %+v", current)
	}
	calls := len(client.snapshot())
	if err := restore(); err != nil || len(client.snapshot()) != calls {
		t.Fatalf("repeated restoration issued another command: %v", err)
	}
}

func TestDomainRestoreBounded(t *testing.T) {
	for _, operation := range []string{"enable", "disable"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				browser, _ := newEventTestBrowser(t)
				client := new(sessionTestClient)
				browser.Client(client)
				change := browser.EnableDomain
				if operation == "disable" {
					if err := (proto.RuntimeEnable{}).Call(browser); err != nil {
						t.Fatal(err)
					}
					change = browser.DisableDomain
				}
				restore, err := change("", &proto.RuntimeEnable{})
				if err != nil {
					t.Fatal(err)
				}
				client.call = func(ctx context.Context, _, _ string, _ any) ([]byte, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				start := time.Now()
				if err := restore(); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("stalled restoration: %v", err)
				}
				if elapsed := time.Since(start); elapsed != 5*time.Second {
					t.Fatalf("restoration budget: %s", elapsed)
				}
			})
		})
	}
}

func TestSetExtraHeadersDomainErrors(t *testing.T) {
	for _, method := range []string{"Network.enable", "Network.setExtraHTTPHeaders", "Network.disable"} {
		t.Run(method, func(t *testing.T) {
			browser, _ := newEventTestBrowser(t)
			failed := errors.New("protocol request failed")
			client := &sessionTestClient{call: func(_ context.Context, _, request string, _ any) ([]byte, error) {
				if request == method {
					return nil, failed
				}
				return []byte(`{}`), nil
			}}
			browser.Client(client)
			page := &Page{browser: browser, ctx: t.Context(), SessionID: "session"}
			restore, err := page.SetExtraHeaders([]string{"X-Example", "value"})
			if method == "Network.disable" {
				if err != nil {
					t.Fatal(err)
				}
				if err := restore(); !errors.Is(err, failed) {
					t.Fatalf("header cleanup error was lost: %v", err)
				}
				return
			}
			if restore != nil || !errors.Is(err, failed) {
				t.Fatalf("header setup result: restore=%t, err=%v", restore != nil, err)
			}
			if page.LoadState(&proto.NetworkEnable{}) {
				t.Fatal("failed header setup retained Network enablement")
			}
			if method == "Network.enable" {
				for _, call := range client.snapshot() {
					if call.method == "Network.setExtraHTTPHeaders" {
						t.Fatal("headers were configured after Network setup failed")
					}
				}
			}
		})
	}
}
