package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
)

type captureLifecycleClient struct {
	t              *testing.T
	events         chan *cdp.Event
	closeRequested chan struct{}
	closeTarget    func(context.Context) ([]byte, error)
	stallBody      bool
}

func (c *captureLifecycleClient) Event() <-chan *cdp.Event { return c.events }

func (c *captureLifecycleClient) Call(ctx context.Context, _ string, method string, _ any) ([]byte, error) {
	switch method {
	case "Target.setDiscoverTargets", "Page.enable", "Fetch.enable":
		return []byte(`{}`), nil
	case "Target.attachToTarget":
		return []byte(`{"sessionId":"capture-session"}`), nil
	case "Fetch.getResponseBody":
		if c.stallBody {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, io.EOF
	case "Target.closeTarget":
		close(c.closeRequested)
		if c.closeTarget != nil {
			return c.closeTarget(ctx)
		}
		return []byte(`{"success":true}`), nil
	default:
		c.t.Errorf("unexpected protocol call: %s", method)
		return nil, fmt.Errorf("unexpected protocol call: %s", method)
	}
}

func newCaptureLifecyclePage(t *testing.T) (*rod.Page, *captureLifecycleClient) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	client := &captureLifecycleClient{
		t: t, events: make(chan *cdp.Event), closeRequested: make(chan struct{}),
	}
	browser := rod.New().ControlURL("").NoDefaultDevice().Context(ctx).Client(client)
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}
	page, err := browser.PageFromTarget("capture-target")
	if err != nil {
		t.Fatal(err)
	}
	return page, client
}

func startCaptureLifecycle(page *rod.Page, client *captureLifecycleClient) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := captureResponse(page, "https://example.test/body", func(page *rod.Page) error {
			client.events <- &cdp.Event{
				SessionID: "capture-session", Method: "Fetch.requestPaused",
				Params: json.RawMessage(`{"requestId":"request","request":{"url":"https://example.test/body","method":"GET","headers":{},"initialPriority":"High","referrerPolicy":"no-referrer"},"frameId":"capture-target","resourceType":"Fetch","responseStatusCode":200}`),
			}
			<-page.GetContext().Done()
			return page.GetContext().Err()
		})
		done <- err
	}()
	return done
}

func (c *captureLifecycleClient) destroy(target string) {
	c.events <- &cdp.Event{
		Method: "Target.targetDestroyed",
		Params: json.RawMessage(fmt.Sprintf(`{"targetId":%q}`, target)),
	}
}

func TestCaptureResponseWaitsForTargetDestruction(t *testing.T) {
	for _, body := range []string{"ambiguous", "stalled"} {
		t.Run(body, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				page, client := newCaptureLifecyclePage(t)
				client.stallBody = body == "stalled"
				done := startCaptureLifecycle(page, client)
				<-client.closeRequested
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("capture returned before target destruction: %v", err)
				default:
				}
				client.destroy("another-target")
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("another target's destruction ended cleanup: %v", err)
				default:
				}
				client.destroy("capture-target")
				err := <-done
				want := io.EOF
				if client.stallBody {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, errIncompleteBody) || !errors.Is(err, want) || strings.Contains(err.Error(), "dedicated capture page") {
					t.Fatalf("capture error after destruction: %v", err)
				}
			})
		})
	}
}

func TestCaptureResponseDestructionBeforeCloseAcknowledgement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newCaptureLifecyclePage(t)
		client.closeTarget = func(context.Context) ([]byte, error) {
			client.destroy("capture-target")
			// Deliver the event while the close command is still outstanding.
			synctest.Wait()
			return []byte(`{"success":true}`), nil
		}
		err := <-startCaptureLifecycle(page, client)
		if !errors.Is(err, errIncompleteBody) || !errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("destruction before acknowledgement: %v", err)
		}
	})
}

func TestCaptureResponseDestructionDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newCaptureLifecyclePage(t)
		err := <-startCaptureLifecycle(page, client)
		if !errors.Is(err, errIncompleteBody) || !errors.Is(err, io.EOF) || !errors.Is(err, context.DeadlineExceeded) ||
			!strings.Contains(err.Error(), "wait for dedicated capture page capture-target destruction") {
			t.Fatalf("missing destruction event: %v", err)
		}
	})
}

func TestCaptureResponseCloseError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newCaptureLifecyclePage(t)
		client.closeTarget = func(context.Context) ([]byte, error) { return nil, io.ErrClosedPipe }
		err := <-startCaptureLifecycle(page, client)
		if !errors.Is(err, errIncompleteBody) || !errors.Is(err, io.EOF) || !errors.Is(err, io.ErrClosedPipe) ||
			errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "close dedicated capture page capture-target") {
			t.Fatalf("close command failure: %v", err)
		}
	})
}
