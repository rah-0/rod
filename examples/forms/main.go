// Run with: go run ./examples/forms
// Fill and submit a real multipart form on a local HTTP fixture.
package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

const uploadContent = "A file uploaded by Rod.\n"

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	browser := rod.New().Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.Close()) }()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><title>Upload form</title>
<button id="open" type="button" onclick="document.querySelector('#form').hidden = false">Open form</button>
<form id="form" hidden method="POST" action="/upload" enctype="multipart/form-data">
  <label>Title <input name="title"></label>
  <label>Attachment <input name="upload" type="file"></label>
  <button id="submit" type="submit">Submit</button>
</form>
<script>alert('Welcome'); document.body.dataset.ready = 'true';</script>`)
	})
	mux.HandleFunc("POST /upload", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		upload, header, err := r.FormFile("upload")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, readErr := io.ReadAll(upload)
		if err := errors.Join(readErr, upload.Close()); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Upload received</title>
<section id="result" data-method="POST">
<h1 id="received-title">%s</h1><p id="filename">%s</p>
<pre id="content">%s</pre><p id="bytes">%d</p>
</section>`, html.EscapeString(r.FormValue("title")), html.EscapeString(header.Filename), html.EscapeString(string(data)), len(data))
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	app, err := fixture.New(browser, mux, func(page *rod.Page) error {
		// This runs in each new document before its scripts. The fixture owns
		// the page, so closing it also removes this initialization script.
		_, err := page.EvalOnNewDocument(`window.suppressedAlerts = 0;
window.alert = () => { window.suppressedAlerts++ };`)
		return err
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close()) }()
	if err := app.Page.Wait(rod.Eval(`() => document.body.dataset.ready === 'true'`)); err != nil {
		return err
	}
	var suppressed int
	if err := app.Page.EvalJSON(&suppressed, `() => window.suppressedAlerts`); err != nil {
		return err
	}
	if suppressed != 1 {
		return fmt.Errorf("initial alert was not suppressed: %d", suppressed)
	}

	open, err := app.Page.Element("#open")
	if err != nil {
		return err
	}
	if err := open.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	form, err := app.Page.Element("#form")
	if err != nil {
		return err
	}
	if err := form.WaitVisible(); err != nil {
		return err
	}
	title, err := form.Element(`input[name="title"]`)
	if err != nil {
		return err
	}
	if err := title.Input("Release notes"); err != nil {
		return err
	}
	attachment, err := form.Element(`input[name="upload"]`)
	if err != nil {
		return err
	}
	if err := attachment.SetFilesFromMemory([]rod.FilePayload{{
		Name: "note.txt", MIMEType: "text/plain", Data: []byte(uploadContent),
	}}); err != nil {
		return err
	}
	submit, err := form.Element("#submit")
	if err != nil {
		return err
	}
	if err := submit.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	// Finding the result waits for the POST's new document, rather than
	// assuming that clicking a submit button has completed navigation.
	result, err := app.Page.Element("#result")
	if err != nil {
		return err
	}
	if err := result.WaitVisible(); err != nil {
		return err
	}
	var received struct {
		Title    string `json:"title"`
		Filename string `json:"filename"`
		Content  string `json:"content"`
		Bytes    int    `json:"bytes"`
		Method   string `json:"method"`
	}
	if err := app.Page.EvalJSON(&received, `() => ({
  title: document.querySelector('#received-title').textContent,
  filename: document.querySelector('#filename').textContent,
  content: document.querySelector('#content').textContent,
  bytes: Number(document.querySelector('#bytes').textContent),
  method: document.querySelector('#result').dataset.method
})`); err != nil {
		return err
	}
	if received.Title != "Release notes" || received.Filename != "note.txt" || received.Content != uploadContent || received.Bytes != len(uploadContent) || received.Method != "POST" {
		return fmt.Errorf("unexpected submitted form: %+v", received)
	}
	_, err = fmt.Fprintf(output, "Initial alerts suppressed: %d\nSubmitted by %s: %s\nUploaded: %s (%d bytes)\nContent: %s", suppressed, received.Method, received.Title, received.Filename, received.Bytes, received.Content)
	return err
}
