// Run with: go run ./examples/download
// Wait for a browser download and validate its saved bytes.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	directory, err := os.MkdirTemp("", "rod-download-example-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(directory)) }()
	browser := rod.New().ControlURL("").Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.CloseWithTimeout(5*time.Second)) }()

	const report = "item,quantity\nnotebook,2\npencil,3\n"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><a id="download" href="/report.csv">Download report</a>`)
	})
	mux.HandleFunc("GET /report.csv", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="report.csv"`)
		_, _ = io.WriteString(w, report)
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	app, err := fixture.New(browser, mux, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, app.Close()) }()
	link, err := app.Page.Element("#download")
	if err != nil {
		return err
	}

	// Subscribe before clicking. The helper waits for completion and names the
	// saved file by its download GUID, independently of SuggestedFilename.
	wait, err := browser.WaitDownload(directory)
	if err != nil {
		return err
	}
	if err := link.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	download, err := wait()
	if err != nil {
		return err
	}
	if download == nil {
		return fmt.Errorf("browser reported no completed download")
	}
	data, err := os.ReadFile(filepath.Join(directory, download.GUID))
	if err != nil {
		return err
	}
	if download.SuggestedFilename != "report.csv" || !bytes.Equal(data, []byte(report)) {
		return fmt.Errorf("unexpected downloaded file %q: %q", download.SuggestedFilename, data)
	}
	_, err = fmt.Fprintf(output, "Downloaded %s:\n%s", download.SuggestedFilename, data)
	return err
}
