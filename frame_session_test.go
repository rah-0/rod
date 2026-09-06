package rod_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

func TestCrossProcessFrameSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	l := launcher.New().NoSandbox(true).Delete("disable-site-isolation-trials").Set("disable-features", "TranslateUI").Set("site-per-process")
	browser := rod.New().Context(ctx).NoDefaultDevice()
	if err := browser.Launch(l); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := browser.Close(); err != nil {
			t.Error(err)
		}
	}()
	nested := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><button onclick="this.textContent='nested clicked'">Nested</button>`)
	}))
	defer nested.Close()
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><style>body{margin:0}button{width:100px;height:50px}iframe{width:200px;height:80px}</style><button onclick="this.textContent='clicked'">Child</button><iframe src="%s/"></iframe>`, nested.URL)
	}))
	defer child.Close()
	childURL := strings.Replace(child.URL, "127.0.0.1", "localhost", 1)
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/local" {
			fmt.Fprint(w, `<!doctype html><button>Local</button>`)
			return
		}
		fmt.Fprintf(w, `<!doctype html><title>Parent</title><style>body{margin:0}iframe{margin:70px;border:5px solid;width:300px;height:200px}</style><iframe src="%s/"></iframe>`, childURL)
	}))
	defer parent.Close()
	p, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Navigate(parent.URL + "/"); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitLoad(); err != nil {
		t.Fatal(err)
	}
	frameElement, err := p.Element("iframe")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := frameElement.Frame()
	if err != nil {
		t.Fatal(err)
	}
	if frame.SessionID == p.SessionID {
		t.Fatal("fixture did not create a cross-process frame")
	}
	if frame.GetContext() != frameElement.GetContext() {
		t.Fatal("frame lost caller context")
	}
	button, err := frame.Element("button")
	if err != nil {
		t.Fatal(err)
	}
	if text, err := button.Text(); err != nil || text != "Child" {
		t.Fatalf("child query: %q, %v", text, err)
	}
	if err := button.Click(proto.InputMouseButtonLeft, 1); err != nil {
		t.Fatal(err)
	}
	if text, err := button.Text(); err != nil || text != "clicked" {
		t.Fatalf("child click: %q, %v", text, err)
	}
	nestedElement, err := frame.Element("iframe")
	if err != nil {
		t.Fatal(err)
	}
	nestedFrame, err := nestedElement.Frame()
	if err != nil {
		t.Fatal(err)
	}
	if nestedFrame.SessionID == frame.SessionID {
		t.Fatal("nested frame did not receive its own renderer session")
	}
	nestedButton, err := nestedFrame.Element("button")
	if err != nil {
		t.Fatal(err)
	}
	if err := nestedButton.Click(proto.InputMouseButtonLeft, 1); err != nil {
		t.Fatal(err)
	}
	if text, err := nestedButton.Text(); err != nil || text != "nested clicked" {
		t.Fatalf("nested child click: %q, %v", text, err)
	}
	if err := frame.Navigate(childURL + "/next"); err != nil {
		t.Fatal(err)
	}
	if err := frame.WaitLoad(); err != nil {
		t.Fatal(err)
	}
	if text, err := frame.Element("button"); err != nil {
		t.Fatal(err)
	} else if value, err := text.Text(); err != nil || value != "Child" {
		t.Fatalf("child navigation: %q, %v", value, err)
	}
	if title, err := p.Eval(`() => document.title`); err != nil || title.Value.Str() != "Parent" {
		t.Fatalf("child navigation changed the parent: title=%+v error=%v", title, err)
	}
	if _, err := frameElement.Eval(`url => this.src = url`, parent.URL+"/local"); err != nil {
		t.Fatal(err)
	}
	if err := frameElement.Wait(rod.Eval(`() => this.contentDocument?.querySelector('button')?.textContent === 'Local'`)); err != nil {
		t.Fatal(err)
	}
	assertClosed := func(old *rod.Page) {
		t.Helper()
		events := old.Event()
		for {
			select {
			case _, open := <-events:
				if open {
					continue
				}
				probeCtx, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				_, err := old.Context(probeCtx).Eval(`() => 1`)
				if err == nil || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
					t.Fatalf("obsolete renderer session did not terminate promptly: %v", err)
				}
				return
			case <-ctx.Done():
				t.Fatal("obsolete renderer session stayed alive")
			}
		}
	}
	assertClosed(frame)
	local, err := frameElement.Frame()
	if err != nil {
		t.Fatal(err)
	}
	if local.SessionID != p.SessionID {
		t.Fatal("same-process navigation retained the old renderer session")
	}
	if text, err := local.Eval(`() => document.querySelector('button').textContent`); err != nil || text.Value.Str() != "Local" {
		t.Fatalf("same-process frame evaluation: %+v, %v", text, err)
	}
	waitTarget := browser.EachEvent(rod.On(func(event *proto.TargetTargetCreated, _ proto.TargetSessionID) bool {
		return event.TargetInfo.TargetID == proto.TargetTargetID(local.FrameID)
	}))
	if err := local.Navigate(childURL + "/return"); err != nil {
		t.Fatal(err)
	}
	if err := waitTarget(); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Eval(`() => document.title`); !errors.Is(err, rod.ErrFrameContextChanged) {
		t.Fatalf("obsolete same-process frame did not report its renderer change: %v", err)
	}
	frame, err = frameElement.Frame()
	if err != nil {
		t.Fatal(err)
	}
	if frame.SessionID == p.SessionID {
		t.Fatal("renderer transition did not attach the new cross-process session")
	}
	if _, err := frame.Element("button"); err != nil {
		t.Fatal(err)
	}
	if err := frameElement.Remove(); err != nil {
		t.Fatal(err)
	}
	assertClosed(frame)
}
