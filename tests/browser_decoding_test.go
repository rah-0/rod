package rod_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

// decodingClient answers Target.getTargetInfo with a target that lacks the
// required type, title, url and attached, and other commands with enough data
// to create an element. It forwards the events sent to it.
type decodingClient struct{ events chan *cdp.Event }

func (c *decodingClient) Event() <-chan *cdp.Event { return c.events }

func (c *decodingClient) Call(_ context.Context, _, method string, _ any) ([]byte, error) {
	switch method {
	case "Target.getTargetInfo":
		return []byte(`{"targetInfo":{"targetId":"page"}}`), nil
	case "Runtime.evaluate":
		return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
	case "Runtime.callFunctionOn":
		return []byte(`{"result":{"type":"undefined"}}`), nil
	}
	return []byte(`{}`), nil
}

// loadingFinished embeds a protocol event, so Message.Load decodes it with
// encoding/json.
type loadingFinished struct{ proto.NetworkLoadingFinished }

// requireMissingField fails unless err reports that typ lacks the field at path.
func requireMissingField(t *testing.T, err error, typ, path string) {
	t.Helper()
	missing, ok := errors.AsType[*proto.MissingFieldError](err)
	if !ok || !errors.Is(err, proto.ErrMissingField) || missing.Type != typ || missing.Path != path {
		t.Fatalf("error = %v, want %s without %s", err, typ, path)
	}
}

// Browser.Decoding applies to the command results of the browser, its pages
// and elements, and to the events it receives, whichever type a subscriber
// loads first. Strict decoding rejects missing required fields; lenient
// decoding keeps their zero values.
func TestBrowserDecoding(t *testing.T) {
	for name, mode := range map[string]proto.Decoding{"strict": proto.DecodeStrict, "lenient": proto.DecodeLenient} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := &decodingClient{events: make(chan *cdp.Event)}
				browser := rod.New().Context(t.Context()).Monitor("").Client(client)
				if browser.GetDecoding() != proto.DecodeStrict {
					t.Fatal("decoding is not strict by default")
				}
				if err := browser.Decoding(mode).Connect(); err != nil {
					t.Fatal(err)
				}
				page := browser.PageFromSession("session")
				element, err := page.ElementFromObject(&proto.RuntimeRemoteObject{Type: "object", ObjectID: "element"})
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range []proto.Client{browser, page, element} {
					if got := c.(proto.Decodable).GetDecoding(); got != mode {
						t.Fatalf("%T decoding = %v", c, got)
					}
					info, err := proto.TargetGetTargetInfo{}.Call(c)
					if mode == proto.DecodeStrict {
						requireMissingField(t, err, "TargetGetTargetInfoResult", "targetInfo.type")
					} else if err != nil || info.TargetInfo.TargetID != "page" || info.TargetInfo.Type != "" {
						t.Fatalf("%T result = %+v, %v", c, info, err)
					}
				}

				incomplete := &cdp.Event{SessionID: "session", Method: "Network.loadingFinished", Params: json.RawMessage(`{"requestId":"request"}`)}
				var finished proto.NetworkLoadingFinished
				wait := browser.WaitEvent(&finished)
				client.events <- incomplete
				err = wait()
				if mode == proto.DecodeStrict {
					requireMissingField(t, err, "NetworkLoadingFinished", "timestamp")
				} else if err != nil || finished.RequestID != "request" || finished.EncodedDataLength != 0 {
					t.Fatalf("event = %+v, %v", finished, err)
				}

				// A type from another package that is loaded first does not
				// change the decoding of the protocol type.
				messages := browser.Event()
				client.events <- incomplete
				message := <-messages
				var wrapper loadingFinished
				if ok, err := message.Load(&wrapper); !ok || err != nil || wrapper.RequestID != "request" {
					t.Fatalf("Load = %t, %v; event %+v", ok, err, wrapper)
				}
				finished = proto.NetworkLoadingFinished{}
				ok, err := message.Load(&finished)
				if mode == proto.DecodeStrict {
					requireMissingField(t, err, "NetworkLoadingFinished", "timestamp")
				} else if !ok || err != nil || finished.RequestID != "request" {
					t.Fatalf("Load = %t, %v; event %+v", ok, err, finished)
				}
			})
		})
	}
}
