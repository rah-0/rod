package rod

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

func TestFrameContextErrorPreservesCause(t *testing.T) {
	for _, method := range []string{"DOM.describeNode", "DOM.resolveNode"} {
		for _, failure := range []struct {
			name    string
			err     error
			changed bool
		}{
			{"stale document", &cdp.Error{Code: -32000, Message: "Node with given id does not belong to the document"}, true},
			{"other protocol error", &cdp.Error{Code: -32000, Message: "unrelated protocol failure"}, false},
			{"cancellation", context.Canceled, false},
		} {
			t.Run(method+"/"+failure.name, func(t *testing.T) {
				browser := New().Context(t.Context()).Client(&lifecycleRegressionClient{call: func(_ context.Context, called string, _ any) ([]byte, error) {
					if called == method {
						return nil, failure.err
					}
					if called == "DOM.describeNode" {
						return []byte(`{"node":{"contentDocument":{"backendNodeId":2}}}`), nil
					}
					t.Fatalf("unexpected request: %s", called)
					return nil, nil
				}})
				parent := &Page{browser: browser, ctx: t.Context(), SessionID: "session"}
				frame := &Page{
					browser: browser, ctx: t.Context(), SessionID: "session", FrameID: "child",
					jsCtxLock: new(sync.Mutex), jsCtxID: new(proto.RuntimeRemoteObjectID), helpers: &jsHelperCache{},
					element: &Element{page: parent, ctx: t.Context(), Object: &proto.RuntimeRemoteObject{ObjectID: "iframe"}},
				}
				_, err := frame.getJSCtxID()
				if !errors.Is(err, failure.err) {
					t.Fatalf("lost original error: %v", err)
				}
				if changed := errors.Is(err, ErrFrameContextChanged); changed != failure.changed {
					t.Fatalf("context changed classification = %v, want %v: %v", changed, failure.changed, err)
				}
				if !failure.changed && err != failure.err {
					t.Fatalf("unrelated error was wrapped: %v", err)
				}
			})
		}
	}
}
