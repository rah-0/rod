package rod_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

func TestCanvasToImageRejectsInvalidDataURL(t *testing.T) {
	g := setup(t)
	p := g.page.MustNavigate(g.srcFile("fixtures/canvas.html"))
	el := p.MustElement("#canvas")
	for _, value := range []any{"not a data URL", "data:image/png;base64", "data:image/png;base64,@@@@", 42, nil} {
		el.MustEval(`value => { this.toDataURL = () => value }`, value)
		if data, err := el.CanvasToImage("", -1); !errors.Is(err, rod.ErrInvalidDataURL) || data != nil {
			t.Fatalf("toDataURL returning %#v: %d bytes, error %v", value, len(data), err)
		}
		g.Panic(func() { el.MustCanvasToImage() })
	}

	el.MustEval(`() => { this.toDataURL = () => "data:text/plain,a%20b" }`)
	g.Eq(string(el.MustCanvasToImage()), "a b")

	empty := p.MustElementByJS(`() => {
		const canvas = document.createElement("canvas")
		canvas.width = 0
		return document.body.appendChild(canvas)
	}`)
	data, err := empty.CanvasToImage("", -1)
	g.E(err)
	g.Len(data, 0)
}

func TestGetResourceRejectsInvalidBase64(t *testing.T) {
	g := setup(t)
	el := g.page.MustNavigate(g.srcFile("fixtures/resource.html")).MustElement("img")
	g.mc.stub(1, proto.PageGetResourceContent{}, func(StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(proto.PageGetResourceContentResult{Content: "@", Base64Encoded: true}), nil
	})
	var corrupt base64.CorruptInputError
	if data, err := el.Resource(); !errors.As(err, &corrupt) || data != nil {
		t.Fatalf("invalid base64 resource: %d bytes, error %v", len(data), err)
	}
}

func TestElementsRejectAccessorProperties(t *testing.T) {
	g := setup(t)
	p := g.page.MustNavigate(g.blank())
	_, err := p.ElementsByJS(rod.Eval(`() => {
		const list = [document.body]
		Object.defineProperty(list, "extra", {get: () => document.body})
		return list
	}`))
	g.Is(err, &rod.ExpectElementsError{})

	// The page controls the DOM methods used by the query helpers.
	p.MustEval(`() => {
		const query = document.querySelectorAll.bind(document)
		document.querySelectorAll = selector => {
			const list = Array.from(query(selector))
			Object.defineProperty(list, "extra", {get: () => null})
			return list
		}
	}`)
	_, err = p.Elements("body")
	g.Is(err, &rod.ExpectElementsError{})
}

func TestSearchRetriesEmptySearchResults(t *testing.T) {
	g := setup(t)
	p := g.page.MustNavigate(g.srcFile("fixtures/click.html"))
	g.mc.stub(1, proto.DOMGetSearchResults{}, func(StubSend) (jsonvalue.Value, error) {
		return jsonvalue.New(proto.DOMGetSearchResultsResult{NodeIDs: []proto.DOMNodeID{}}), nil
	})
	g.Eq(p.MustSearch("click me").MustText(), "click me")
}

func TestPagePDFSavesInFewReads(t *testing.T) {
	g := setup(t)
	p := g.page.MustNavigate(g.blank())
	// Noise does not compress, so each canvas adds about 3 MB to the PDF.
	p.MustEval(`() => {
		for (let i = 0; i < 2; i++) {
			const canvas = document.createElement("canvas")
			canvas.width = canvas.height = 1000
			const context = canvas.getContext("2d")
			const image = context.createImageData(canvas.width, canvas.height)
			const data = new Uint8Array(image.data.buffer)
			for (let offset = 0; offset < data.length; offset += 65536) {
				crypto.getRandomValues(data.subarray(offset, offset + 65536))
			}
			context.putImageData(image, 0, 0)
			document.body.appendChild(canvas)
		}
	}`)

	var reads atomic.Int64
	g.mc.setCall(func(ctx context.Context, sessionID, method string, params any) ([]byte, error) {
		if method == (proto.IORead{}).ProtoReq() {
			reads.Add(1)
		}
		return g.mc.principal.Call(ctx, sessionID, method, params)
	})
	defer g.mc.resetCall()
	reader, err := p.PDF(&proto.PagePrintToPDF{})
	g.E(err)
	defer func() { g.E(reader.Close()) }()
	file := filepath.Join(t.TempDir(), "page.pdf")
	g.E(utils.OutputFile(file, reader))

	data, err := os.ReadFile(file)
	g.E(err)
	g.True(bytes.HasPrefix(data, []byte("%PDF-")))
	const chunk = 4 << 20
	if len(data) <= chunk {
		t.Fatalf("PDF has %d bytes, want more than one read", len(data))
	}
	// An extra read may report EOF alone.
	if got, want := reads.Load(), int64(len(data)/chunk+2); got > want {
		t.Fatalf("IO.read calls = %d for %d bytes, want at most %d", got, len(data), want)
	}
}

func TestStreamReaderReturnsLoadingBodyData(t *testing.T) {
	g := setup(t)
	server := g.Serve()
	release := make(chan struct{})
	releaseBody := sync.OnceFunc(func() { close(release) })
	defer releaseBody()
	server.Mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html></html>"))
	})
	server.Mux.HandleFunc("/body", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(bytes.Repeat([]byte("a"), 64<<10))
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = w.Write([]byte("end"))
		case <-r.Context().Done():
		}
	})

	p := g.newPage(server.URL("/page")).MustWaitLoad()
	g.E(proto.FetchEnable{Patterns: []*proto.FetchRequestPattern{{
		URLPattern:   server.URL("/body"),
		RequestStage: proto.FetchRequestStageResponse,
	}}}.Call(p))
	defer func() { g.E(proto.FetchDisable{}.Call(p)) }()
	paused := proto.FetchRequestPaused{}
	wait := p.WaitEvent(&paused)
	p.MustEval(`url => { fetch(url).catch(() => {}) }`, server.URL("/body"))
	g.E(wait())
	defer func() {
		g.E(proto.FetchFailRequest{RequestID: paused.RequestID, ErrorReason: proto.NetworkErrorReasonAborted}.Call(p))
	}()
	body, err := proto.FetchTakeResponseBodyAsStream{RequestID: paused.RequestID}.Call(p)
	g.E(err)
	reader := rod.NewStreamReader(p, body.Stream)
	defer func() { g.E(reader.Close()) }()

	// The body is still loading, so the first read must not wait for its end.
	buffer := make([]byte, 32<<10)
	read := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(reader, buffer)
		read <- err
	}()
	select {
	case err := <-read:
		g.E(err)
	case <-time.After(10 * time.Second):
		releaseBody()
		<-read
		t.Fatal("reading available data waited for the rest of the body")
	}
	g.True(bytes.Equal(buffer, bytes.Repeat([]byte("a"), len(buffer))))

	releaseBody()
	rest, err := io.ReadAll(reader)
	g.E(err)
	g.Eq(string(rest), strings.Repeat("a", 32<<10)+"end")
}
