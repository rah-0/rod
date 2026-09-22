package rod_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
)

const memoryFileHTML = `<!doctype html>
<form action="/upload" method="post" enctype="multipart/form-data">
  <input id="files" name="upload" type="file" multiple>
</form>
<input id="single" type="file">
<input id="text" type="text" value="unchanged">
<div id="invalid">unchanged</div>
<script>
window.fileEvents = [];
for (const type of ['input', 'change']) {
  document.addEventListener(type, event => window.fileEvents.push({
    type: event.type, bubbles: event.bubbles,
    composed: event.composed, isTrusted: event.isTrusted
  }));
}
</script>`

type memoryFileEvent struct {
	Type      string
	Bubbles   bool
	Composed  bool
	IsTrusted bool
}

type memoryFileValidationCase struct {
	selector string
	files    []rod.FilePayload
}

func TestSetFilesFromMemorySubmission(t *testing.T) {
	for _, connection := range []string{"local", "managed"} {
		t.Run(connection, func(t *testing.T) {
			g := setup(t)
			browser := g.browser
			if connection == "managed" {
				browser = memoryFileManagedBrowser(t, g)
			}
			server := g.Serve().Route("/", "", memoryFileHTML)
			server.Mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
				reader, err := r.MultipartReader()
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				var uploaded []rod.FilePayload
				for {
					part, err := reader.NextPart()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					if part.FormName() != "upload" {
						http.Error(w, "unexpected form field", http.StatusBadRequest)
						return
					}
					data, err := io.ReadAll(part)
					if err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					uploaded = append(uploaded, rod.FilePayload{
						Name: part.FileName(), MIMEType: part.Header.Get("Content-Type"), Data: data,
					})
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(uploaded); err != nil {
					t.Error(err)
				}
			})
			page := browser.MustPage(server.URL()).MustWaitLoad()
			defer page.MustClose()
			input := page.MustElement("#files")
			files := []rod.FilePayload{
				{Name: "empty.txt", MIMEType: "text/plain", Data: []byte{}},
				{Name: "nil.txt", MIMEType: "text/plain"},
				{Name: "résumé-文件.bin", MIMEType: "application/octet-stream", Data: []byte{0, 0xff, 0x80, 0xc0, 1}},
				{Name: "notes.txt", MIMEType: "text/plain", Data: []byte("first line\n日本語\n")},
			}
			if returned := input.MustSetFilesFromMemory(files...); returned != input {
				t.Fatal("MustSetFilesFromMemory did not return the input for chaining")
			}
			var uploaded []rod.FilePayload
			response := page.MustEval(`async () => {
  const form = document.querySelector('form');
  const response = await fetch(form.action, {method: 'POST', body: new FormData(form)});
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}`)
			if err := response.Unmarshal(&uploaded); err != nil {
				t.Fatal(err)
			}
			if len(uploaded) != len(files) {
				t.Fatalf("uploaded %d files, want %d", len(uploaded), len(files))
			}
			for i, want := range files {
				got := uploaded[i]
				if got.Name != want.Name || got.MIMEType != want.MIMEType || !bytes.Equal(got.Data, want.Data) {
					t.Errorf("uploaded file %d = %+v, want %+v", i, got, want)
				}
			}
			checkMemoryFileEvents(t, page)
		})
	}
}

func TestSetFilesFromMemoryValidation(t *testing.T) {
	g := setup(t)
	page := g.page.MustNavigate(g.html(memoryFileHTML)).MustWaitLoad()
	input := page.MustElement("#single")
	original := rod.FilePayload{Name: "original.txt", MIMEType: "text/plain", Data: []byte("original")}
	input.MustSetFilesFromMemory(original)
	page.MustEval(`() => window.fileEvents = []`)
	for _, tc := range []memoryFileValidationCase{
		{selector: "#single", files: []rod.FilePayload{original, original}},
		{selector: "#text", files: []rod.FilePayload{original}},
		{selector: "#invalid", files: nil},
	} {
		err := page.MustElement(tc.selector).SetFilesFromMemory(tc.files)
		if !errors.Is(err, &rod.EvalError{}) {
			t.Errorf("SetFilesFromMemory(%s) = %v, want EvalError", tc.selector, err)
		}
	}
	panicValue := g.Panic(func() { page.MustElement("#invalid").MustSetFilesFromMemory(original) })
	if err, ok := panicValue.(error); !ok || !errors.Is(err, &rod.EvalError{}) {
		t.Fatalf("MustSetFilesFromMemory panic = %v, want EvalError", panicValue)
	}
	for _, end := range []string{"cancellation", "deadline"} {
		ctx, cancel := context.WithCancel(t.Context())
		wantErr := context.Canceled
		cancel()
		if end == "deadline" {
			ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			wantErr = context.DeadlineExceeded
		}
		err := input.Context(ctx).SetFilesFromMemory(nil)
		cancel()
		if !errors.Is(err, wantErr) {
			t.Errorf("%s = %v, want %v", end, err, wantErr)
		}
	}
	if got := input.MustEval(`async () => Array.from(this.files).map(file => file.name)`).JSON("", ""); got != `["original.txt"]` {
		t.Fatalf("failed call changed the file list: %s", got)
	}
	if got := input.MustEval(`() => this.files[0].text()`).Str(); got != string(original.Data) {
		t.Fatalf("failed call changed the file content: %q", got)
	}
	if got := page.MustEval(`() => window.fileEvents.length`).Int(); got != 0 {
		t.Fatalf("failed calls dispatched %d events", got)
	}
	if got := page.MustElement("#text").MustProperty("value").Str(); got != "unchanged" {
		t.Fatalf("invalid input value changed: %q", got)
	}
	if got := page.MustElement("#invalid").MustText(); got != "unchanged" {
		t.Fatalf("invalid element text changed: %q", got)
	}

	replacement := rod.FilePayload{Name: "replacement.txt", MIMEType: "text/plain", Data: []byte("replacement")}
	input.MustSetFilesFromMemory(replacement)
	if got := input.MustEval(`async () => Array.from(this.files).map(file => file.name)`).JSON("", ""); got != `["replacement.txt"]` {
		t.Fatalf("file list was not replaced: %s", got)
	}
	checkMemoryFileEvents(t, page)
	for _, files := range [][]rod.FilePayload{nil, {}} {
		input.MustSetFilesFromMemory(original)
		page.MustEval(`() => window.fileEvents = []`)
		if err := input.SetFilesFromMemory(files); err != nil {
			t.Fatal(err)
		}
		if got := input.MustEval(`() => this.files.length`).Int(); got != 0 {
			t.Fatalf("empty payload list retained %d files", got)
		}
		checkMemoryFileEvents(t, page)
	}
}

func TestSetFilesFromMemoryFrame(t *testing.T) {
	g := setup(t)
	page := g.page.MustNavigate(g.html(`<iframe srcdoc='<input type="file">'></iframe>`)).MustWaitLoad()
	input := page.MustElement("iframe").MustFrame().MustElement("input")
	input.MustSetFilesFromMemory(rod.FilePayload{Name: "frame.bin", MIMEType: "application/octet-stream", Data: []byte{0xff, 0}})
	if !input.MustEval(`() => this.files[0] instanceof this.ownerDocument.defaultView.File`).Bool() {
		t.Fatal("file was constructed outside the input's realm")
	}
	if got := input.MustEval(`async () => Array.from(new Uint8Array(await this.files[0].arrayBuffer()))`).JSON("", ""); got != `[255,0]` {
		t.Fatalf("frame file bytes = %s", got)
	}
}

func checkMemoryFileEvents(t *testing.T, page *rod.Page) {
	t.Helper()
	var events []memoryFileEvent
	if err := page.MustEval(`() => window.fileEvents`).Unmarshal(&events); err != nil {
		t.Fatal(err)
	}
	want := []memoryFileEvent{
		{Type: "input", Bubbles: true, Composed: true},
		{Type: "change", Bubbles: true},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("file events = %+v, want %+v", events, want)
	}
}

func memoryFileManagedBrowser(t *testing.T, g G) *rod.Browser {
	t.Helper()
	const token = "memory-file-test-token"
	manager := launcher.NewManager(token)
	finished := make(chan struct{})
	server := g.Serve()
	server.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			defer close(finished)
		}
		manager.ServeHTTP(w, r)
	})
	ctx := g.Timeout(*TimeoutEach)
	remote := launcher.MustNewManaged(ctx, server.URL(), token).NoSandbox(true)
	client := remote.MustClient()
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("managed browser did not finish cleanup")
		}
	})
	return rod.New().NoDefaultDevice().Context(ctx).Client(client).MustConnect()
}
