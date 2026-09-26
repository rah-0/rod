package main_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
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

// benchmarkDOMPage creates a page with the given number of DOM nodes: elements
// that each hold one distinct text node.
func benchmarkDOMPage(b *testing.B, browser *rod.Browser, nodes int) *rod.Page {
	b.Helper()
	page := browser.MustPage("about:blank").Context(b.Context())
	page.MustEval(`nodes => {
		const parts = [];
		for (let i = 0; i < nodes / 2; i++) parts.push('<div class="c' + i % 10 + '">item ' + i + '</div>');
		document.body.innerHTML = parts.join('');
	}`, nodes)
	if count := page.MustEval(`() => document.body.getElementsByTagName('*').length * 2`).Int(); count != nodes {
		b.Fatalf("page has %d nodes, want %d", count, nodes)
	}
	if err := (proto.PerformanceEnable{}).Call(page); err != nil {
		b.Fatal(err)
	}
	return page
}

// rendererTaskDuration returns the main-thread task time of the page's renderer.
func rendererTaskDuration(b *testing.B, page *rod.Page) time.Duration {
	b.Helper()
	res, err := (proto.PerformanceGetMetrics{}).Call(page)
	if err != nil {
		b.Fatal(err)
	}
	for _, metric := range res.Metrics {
		if metric.Name == "TaskDuration" {
			return time.Duration(metric.Value * float64(time.Second))
		}
	}
	b.Fatal("TaskDuration metric is unavailable")
	return 0
}

// browserCPUTime sums the user and system CPU time of the live browser process
// tree on Linux. It returns false when /proc is unavailable.
func browserCPUTime(root int) (time.Duration, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	parents := map[int]int{}
	ticks := map[int]int64{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		// After the parenthesized command: state, ppid, ..., utime (14), stime (15).
		end := bytes.LastIndexByte(data, ')')
		if end < 0 {
			continue
		}
		fields := bytes.Fields(data[end+1:])
		if len(fields) < 13 {
			continue
		}
		ppid, _ := strconv.Atoi(string(fields[1]))
		utime, _ := strconv.ParseInt(string(fields[11]), 10, 64)
		stime, _ := strconv.ParseInt(string(fields[12]), 10, 64)
		parents[pid] = ppid
		ticks[pid] = utime + stime
	}
	if _, ok := parents[root]; !ok {
		return 0, false
	}
	var total int64
	for pid, value := range ticks {
		for p := pid; p > 1; p = parents[p] {
			if p == root {
				total += value
				break
			}
		}
	}
	// Linux reports these fields in USER_HZ, which is 100 on supported platforms.
	return time.Duration(total) * 10 * time.Millisecond, true
}

// domStableCost accumulates protocol and browser-side costs between start and report.
type domStableCost struct {
	counter      *countingClient
	pages        []*rod.Page
	pid          int
	calls, bytes int64
	tasks, cpu   time.Duration
	cpuOK        bool
}

func startDOMStableCost(b *testing.B, counter *countingClient, pid int, pages ...*rod.Page) *domStableCost {
	b.Helper()
	cost := &domStableCost{counter: counter, pages: pages, pid: pid}
	for _, page := range pages {
		cost.tasks -= rendererTaskDuration(b, page)
	}
	cpu, ok := browserCPUTime(pid)
	cost.cpu, cost.cpuOK = -cpu, ok
	cost.calls = -counter.calls.Load()
	cost.bytes = -counter.bytes.Load()
	return cost
}

func (cost *domStableCost) report(b *testing.B, waits int) {
	b.Helper()
	b.StopTimer()
	cost.calls += cost.counter.calls.Load()
	cost.bytes += cost.counter.bytes.Load()
	cpu, ok := browserCPUTime(cost.pid)
	for _, page := range cost.pages {
		cost.tasks += rendererTaskDuration(b, page)
	}
	total := float64(b.N * waits)
	b.ReportMetric(float64(cost.calls)/total, "cdp-calls/wait")
	b.ReportMetric(float64(cost.bytes)/total, "cdp-resp-B/wait")
	b.ReportMetric(float64(cost.tasks.Microseconds())/1000/total, "renderer-task-ms/wait")
	if ok && cost.cpuOK {
		b.ReportMetric(float64((cpu+cost.cpu).Milliseconds())/total, "browser-cpu-ms/wait")
	}
}

// BenchmarkWaitDOMStable measures DOM stability waits with a 100 ms stability
// period. Static cases wait on an unchanging DOM, one of them with page scripts
// disabled. Mutation cases change one text node every 5 ms: the burst stops
// after 300 ms and waits with diff 0, while the continuous case never stops and
// tolerates small changes with diff 0.01. Custom metrics report protocol
// traffic and browser-side CPU time per wait.
func BenchmarkWaitDOMStable(b *testing.B) {
	const stable = 100 * time.Millisecond
	for _, nodes := range []int{2000, 20000, 60000} {
		b.Run(fmt.Sprintf("static/nodes=%d", nodes), func(b *testing.B) {
			browser, counter, pid := benchmarkCountingBrowser(b)
			page := benchmarkDOMPage(b, browser, nodes)
			b.ReportAllocs()
			cost := startDOMStableCost(b, counter, pid, page)
			for b.Loop() {
				if err := page.WaitDOMStable(stable, 0); err != nil {
					b.Fatal(err)
				}
			}
			cost.report(b, 1)
		})
	}
	// Without page scripts, the page cannot deliver mutation records, so the
	// wait compares digests of the DOM.
	b.Run("static/nodes=60000/scripts-disabled", func(b *testing.B) {
		browser, counter, pid := benchmarkCountingBrowser(b)
		page := benchmarkDOMPage(b, browser, 60000)
		if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(page); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		cost := startDOMStableCost(b, counter, pid, page)
		for b.Loop() {
			if err := page.WaitDOMStable(stable, 0); err != nil {
				b.Fatal(err)
			}
		}
		cost.report(b, 1)
	})
	b.Run("static/nodes=20000/pages=8", func(b *testing.B) {
		browser, counter, pid := benchmarkCountingBrowser(b)
		pages := make([]*rod.Page, 8)
		for i := range pages {
			pages[i] = benchmarkDOMPage(b, browser, 20000)
		}
		b.ReportAllocs()
		cost := startDOMStableCost(b, counter, pid, pages...)
		for b.Loop() {
			var wg sync.WaitGroup
			errs := make([]error, len(pages))
			for i, page := range pages {
				wg.Go(func() { errs[i] = page.WaitDOMStable(stable, 0) })
			}
			wg.Wait()
			for _, err := range errs {
				if err != nil {
					b.Fatal(err)
				}
			}
		}
		cost.report(b, len(pages))
	})
	b.Run("mutating/burst/nodes=2000", func(b *testing.B) {
		browser, counter, pid := benchmarkCountingBrowser(b)
		page := benchmarkDOMPage(b, browser, 2000)
		b.ReportAllocs()
		cost := startDOMStableCost(b, counter, pid, page)
		for b.Loop() {
			page.MustEval(`() => {
				const text = document.body.firstElementChild.firstChild;
				let n = 0;
				const timer = setInterval(() => text.data = 'burst ' + ++n, 5);
				setTimeout(() => clearInterval(timer), 300);
			}`)
			if err := page.WaitDOMStable(stable, 0); err != nil {
				b.Fatal(err)
			}
		}
		cost.report(b, 1)
	})
	b.Run("mutating/continuous/nodes=2000/diff=0.01", func(b *testing.B) {
		browser, counter, pid := benchmarkCountingBrowser(b)
		page := benchmarkDOMPage(b, browser, 2000)
		page.MustEval(`() => {
			const text = document.body.firstElementChild.firstChild;
			let n = 0;
			setInterval(() => text.data = 'tick ' + ++n, 5);
		}`)
		b.ReportAllocs()
		cost := startDOMStableCost(b, counter, pid, page)
		for b.Loop() {
			if err := page.WaitDOMStable(stable, 0.01); err != nil {
				b.Fatal(err)
			}
		}
		cost.report(b, 1)
	})
}
