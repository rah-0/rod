package rod

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"math"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

func TestEventWaitOverflow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, client := newEventTestBrowser(t)
		browser = browser.EventBuffer(2, 1024)
		wait := browser.EachEvent(On(func(*proto.RuntimeConsoleAPICalled, proto.TargetSessionID) bool { return true }))
		for range 3 {
			browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", "", `{"type":"log","args":[],"executionContextId":1,"timestamp":0}`))
		}
		if err := wait(); !errors.Is(err, ErrEventOverflow) {
			t.Fatalf("overflow reported success: %v", err)
		}
		synctest.Wait()
		if browser.event.Len() != 0 {
			t.Fatal("wait retained subscription")
		}
		calls := client.snapshot()
		if len(calls) != 2 || calls[0].method != "Runtime.enable" || calls[1].method != "Runtime.disable" {
			t.Fatalf("domain ownership leaked: %v", calls)
		}
	})
}

func TestEventOverflowDuringCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newEventTestBrowser(t)
		browser = browser.EventBuffer(1, 1024)
		wait := browser.EachEvent(On(func(*proto.RuntimeConsoleAPICalled, proto.TargetSessionID) bool {
			for range 2 {
				browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", "", `{"type":"log","args":[],"executionContextId":1,"timestamp":0}`))
			}
			return true
		}))
		browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", "", `{"type":"log","args":[],"executionContextId":1,"timestamp":0}`))
		if err := wait(); !errors.Is(err, ErrEventOverflow) {
			t.Fatalf("overflow during callback reported success: %v", err)
		}
	})
}

func TestEventStreamOverflowContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		browser, _ := newEventTestBrowser(t)
		ctx, events := browser.EventBuffer(100, 1).EventWithContext()
		browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", "", "{}"))
		synctest.Wait()
		if _, open := <-events; open || !errors.Is(context.Cause(ctx), ErrEventOverflow) {
			t.Fatalf("event stream: open=%v, cause=%v", open, context.Cause(ctx))
		}
	})
}

func TestDiagnosticsIngressOverflow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		page, client := newDiagnosticsTestPage(t)
		page = page.EventBuffer(100, 1)
		d, err := page.StartDiagnostics(DiagnosticsOptions{})
		if err != nil {
			t.Fatal(err)
		}
		page.browser.event.Publish(eventTestMessage("Runtime.consoleAPICalled", page.SessionID, `{"type":"log","args":[],"executionContextId":1,"timestamp":0}`))
		synctest.Wait()
		snapshot, err := d.Stop()
		if !errors.Is(err, ErrEventOverflow) || !errors.Is(err, ErrDiagnosticsIncomplete) || len(snapshot.Console) != 0 {
			t.Fatalf("diagnostics overflow: %+v, %v", snapshot, err)
		}
		if page.browser.event.Len() != 0 {
			t.Fatal("diagnostics retained ingress subscription")
		}
		client.mu.Lock()
		defer client.mu.Unlock()
		for domain, active := range client.activeDomains {
			if active {
				t.Errorf("domain %s remained active", domain)
			}
		}
	})
}

type captureStreamClient struct {
	data    string
	encoded bool
	noEOF   bool
	reads   int
	sizes   []int
}

func (*captureStreamClient) Event() <-chan *cdp.Event { return nil }

func (c *captureStreamClient) Call(_ context.Context, _, method string, params any) ([]byte, error) {
	if method == "Page.printToPDF" {
		return []byte(`{"stream":"pdf","data":""}`), nil
	}
	if method == "IO.close" {
		return []byte(`{}`), nil
	}
	req := params.(proto.IORead)
	c.sizes = append(c.sizes, *req.Size)
	c.reads++
	data := c.data
	if c.reads > 1 {
		data = ""
	}
	return json.Marshal(proto.IOReadResult{Data: data, Base64Encoded: c.encoded, EOF: !c.noEOF || c.reads > 1})
}

func TestStreamReaderByteLimit(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		for _, payload := range []string{"1234", "12345", strings.Repeat("x", 10000)} {
			for _, eof := range []bool{false, true} {
				client := &captureStreamClient{data: payload, encoded: encoded, noEOF: !eof}
				if encoded {
					client.data = base64.StdEncoding.EncodeToString([]byte(payload))
				}
				reader := NewStreamReader(client, "stream")
				reader.MaxBytes = 4
				reader.ChunkSize = math.MaxInt
				data, err := io.ReadAll(reader)
				if len(payload) == 4 {
					if err != nil || string(data) != payload {
						t.Fatalf("exact limit, encoded=%v eof=%v: %q, %v", encoded, eof, data, err)
					}
				} else if !errors.Is(err, ErrCaptureTooLarge) || len(data) > 4 {
					t.Fatalf("exceeded limit, encoded=%v eof=%v: %d bytes, %v", encoded, eof, len(data), err)
				} else {
					reads := client.reads
					if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrCaptureTooLarge) || client.reads != reads {
						t.Fatal("read resumed after terminal limit error")
					}
				}
				for _, size := range client.sizes {
					if size <= 0 || size > 5 {
						t.Fatalf("unbounded read size %d", size)
					}
				}
			}
		}
	}
}

func TestStreamReaderLimitExtremes(t *testing.T) {
	client := &captureStreamClient{data: "12"}
	reader := NewStreamReader(client, "stream")
	reader.MaxBytes = math.MaxInt64
	if data, err := io.ReadAll(reader); err != nil || string(data) != "12" || client.sizes[0] <= 0 {
		t.Fatalf("max int limit: %q, %v, sizes %v", data, err, client.sizes)
	}
	reader.MaxBytes = 1
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrCaptureTooLarge) {
		t.Fatalf("lowered limit: %v", err)
	}
	client = &captureStreamClient{data: "%%%", encoded: true}
	reader = NewStreamReader(client, "stream")
	reader.MaxBytes = 4
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("malformed base64 accepted")
	}
}

func TestPDFStreamDefaultLimit(t *testing.T) {
	client := &captureStreamClient{data: "pdf"}
	browser := New().Context(t.Context()).Client(client)
	page := &Page{browser: browser, ctx: t.Context()}
	reader, err := page.PDF(&proto.PagePrintToPDF{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.MaxBytes != MaxCaptureBytes || reader.ChunkSize != pdfChunkSize {
		t.Fatalf("PDF stream limits: %+v", reader)
	}
}

type scrollLimitClient struct {
	eventTestClient
	width, height float64
	calls         []string
}

func (c *scrollLimitClient) Call(_ context.Context, _, method string, _ any) ([]byte, error) {
	c.calls = append(c.calls, method)
	if method != "Page.getLayoutMetrics" {
		return nil, errors.New("capture attempted after oversized layout")
	}
	return json.Marshal(proto.PageGetLayoutMetricsResult{
		LayoutViewport: &proto.PageLayoutViewport{}, VisualViewport: &proto.PageVisualViewport{}, ContentSize: &proto.DOMRect{},
		CSSLayoutViewport: &proto.PageLayoutViewport{},
		CSSVisualViewport: &proto.PageVisualViewport{ClientWidth: c.width, ClientHeight: 800, Scale: 1},
		CSSContentSize:    &proto.DOMRect{Width: c.width, Height: c.height},
	})
}

func TestScrollScreenshotTotalPixelBudget(t *testing.T) {
	client := &scrollLimitClient{width: 32768, height: 32768}
	browser := New().Context(t.Context()).Client(client)
	page := &Page{browser: browser, ctx: t.Context()}
	if _, err := page.ScrollScreenshot(nil); !errors.Is(err, ErrScreenshotTooLarge) {
		t.Fatalf("oversized stitched capture: %v", err)
	}
}

type screenshotSizeCase struct {
	name                 string
	width, height, scale float64
	deviceScale          float64
	allowed              bool
}

func TestScreenshotSizeBudget(t *testing.T) {
	for _, test := range []screenshotSizeCase{
		{name: "exact pixels", width: 8192, height: 4096, scale: 1, allowed: true},
		{name: "excess pixels", width: 8192, height: 4097, scale: 1},
		{name: "exact dimension", width: 32768, height: 1, scale: 1, allowed: true},
		{name: "excess dimension", width: 32769, height: 1, scale: 1},
		{name: "clip scale", width: 4096, height: 4096, scale: 2},
		{name: "device scale", width: 4096, height: 4096, scale: 1, deviceScale: 2},
		{name: "fractional pixels round up", width: 8192.1, height: 4096, scale: 1},
		{name: "zero width", width: 0, height: 1, scale: 1},
		{name: "negative height", width: 1, height: -1, scale: 1},
		{name: "zero scale", width: 1, height: 1},
		{name: "nan width", width: math.NaN(), height: 1, scale: 1},
		{name: "nan height", width: 1, height: math.NaN(), scale: 1},
		{name: "nan scale", width: 1, height: 1, scale: math.NaN()},
		{name: "infinite width", width: math.Inf(1), height: 1, scale: 1},
		{name: "infinite scale", width: 1, height: 1, scale: math.Inf(1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			browser, _ := newEventTestBrowser(t)
			page := &Page{browser: browser, ctx: t.Context()}
			if test.deviceScale > 0 {
				view := proto.EmulationSetDeviceMetricsOverride{DeviceScaleFactor: test.deviceScale}
				browser.setState("", view.ProtoReq(), view, nil)
			}
			err := page.checkScreenshotSize(test.width, test.height, test.scale)
			if test.allowed && err != nil || !test.allowed && !errors.Is(err, ErrScreenshotTooLarge) {
				t.Fatalf("size check: %v, allowed %v", err, test.allowed)
			}
		})
	}
}

func TestScreenshotRejectsBeforeCapture(t *testing.T) {
	for _, fullPage := range []bool{false, true} {
		client := &scrollLimitClient{width: 32768, height: 32768}
		browser := New().Context(t.Context()).Client(client)
		page := &Page{browser: browser, ctx: t.Context()}
		req := &proto.PageCaptureScreenshot{}
		if !fullPage {
			req.Clip = &proto.PageViewport{Width: 32768, Height: 32768, Scale: 1}
		}
		if _, err := page.Screenshot(fullPage, req); !errors.Is(err, ErrScreenshotTooLarge) {
			t.Fatalf("fullPage=%v: %v", fullPage, err)
		}
		if !fullPage && len(client.calls) != 0 || fullPage && (len(client.calls) != 1 || client.calls[0] != "Page.getLayoutMetrics") {
			t.Fatalf("fullPage=%v changed viewport or captured: %v", fullPage, client.calls)
		}
	}
}

func TestScreenshotImageHeaderBudget(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []screenshotSizeCase{
		{name: "exact pixels", width: 8192, height: 4096, allowed: true},
		{name: "excess pixels", width: 8192, height: 4097},
		{name: "excess dimension", width: 32769, height: 1},
	} {
		data := bytes.Clone(encoded.Bytes())
		binary.BigEndian.PutUint32(data[16:20], uint32(test.width))
		binary.BigEndian.PutUint32(data[20:24], uint32(test.height))
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
			t.Fatalf("invalid header fixture: %v", err)
		}
		err := checkScreenshotData(data)
		if test.allowed && err != nil || !test.allowed && !errors.Is(err, ErrScreenshotTooLarge) {
			t.Fatalf("%s: %v, allowed %v", test.name, err, test.allowed)
		}
	}
}
