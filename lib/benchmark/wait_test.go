package main_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
)

func benchmarkWaitPage(b *testing.B) *rod.Page {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)
	launch := launcher.New().Context(ctx)
	b.Cleanup(func() {
		launch.Kill()
		launch.Cleanup()
	})
	url, err := launch.Launch()
	if err != nil {
		b.Fatal(err)
	}
	browser := rod.New().Context(ctx).ControlURL(url)
	if err := browser.Connect(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stop := context.AfterFunc(ctx, launch.Kill)
		defer stop()
		if err := browser.Context(ctx).Close(); err != nil {
			b.Error(err)
		}
	})
	page := browser.MustPage("about:blank").Context(b.Context())
	page.MustEval(`() => true`)
	return page
}

func BenchmarkPageEval(b *testing.B) {
	page := benchmarkWaitPage(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := page.Eval(`() => true`); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPageWait(b *testing.B) {
	for _, delay := range []time.Duration{10 * time.Millisecond, 250 * time.Millisecond} {
		b.Run(delay.String(), func(b *testing.B) {
			page := benchmarkWaitPage(b)
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				page.MustEval(`delay => {
					window.rodBenchmarkReady = false;
					setTimeout(() => window.rodBenchmarkReady = true, delay);
				}`, delay.Milliseconds())
				b.StartTimer()
				if err := page.Wait(rod.Eval(`() => window.rodBenchmarkReady`)); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(delay), "signal-delay-ns/op")
		})
	}
}

func BenchmarkPageElement(b *testing.B) {
	for _, delay := range []time.Duration{10 * time.Millisecond, 250 * time.Millisecond} {
		b.Run(delay.String(), func(b *testing.B) {
			page := benchmarkWaitPage(b)
			page.MustSetDocumentContent(`<span></span>`)
			page.MustElement("span").MustRelease()
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				page.MustEval(`delay => {
					document.body.replaceChildren();
					setTimeout(() => document.body.append(document.createElement('span')), delay);
				}`, delay.Milliseconds())
				b.StartTimer()
				el, err := page.Element("span")
				if err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				if err := el.Release(); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
			b.ReportMetric(float64(delay), "signal-delay-ns/op")
		})
	}
}

func BenchmarkPageWaitInteractive(b *testing.B) {
	b.Run("Ready", func(b *testing.B) {
		page := benchmarkWaitPage(b)
		page.MustWaitLoad()
		page.MustWaitInteractive()
		b.ReportAllocs()
		for b.Loop() {
			if err := page.WaitInteractive(); err != nil {
				b.Fatal(err)
			}
		}
	})
	for _, delay := range []time.Duration{10 * time.Millisecond, 250 * time.Millisecond} {
		b.Run(delay.String(), func(b *testing.B) {
			parsing := make(chan chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/" {
					http.NotFound(w, r)
					return
				}
				finish := make(chan struct{})
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, "<!doctype html><html><body><p>Parsing</p>")
				w.(http.Flusher).Flush()
				select {
				case parsing <- finish:
				case <-r.Context().Done():
					return
				}
				select {
				case <-finish:
					_, _ = io.WriteString(w, "</body></html>")
				case <-r.Context().Done():
				}
			}))
			b.Cleanup(server.Close)
			page := benchmarkWaitPage(b)
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				page.MustNavigate(server.URL)
				finish := <-parsing
				if state := page.MustEval(`() => document.readyState`).Str(); state != "loading" {
					b.Fatalf("document state before releasing response: %s", state)
				}
				b.StartTimer()
				time.AfterFunc(delay, func() { close(finish) })
				if err := page.WaitInteractive(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(delay), "signal-delay-ns/op")
		})
	}
}
