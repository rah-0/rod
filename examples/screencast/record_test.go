package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"image/png"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/goroutines"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

type screencastTestClient struct {
	*cdp.Client
	mu       sync.Mutex
	method   string
	failure  error
	after    bool
	commands []string
	acked    chan struct{}
}

func (c *screencastTestClient) Call(ctx context.Context, session, method string, params any) ([]byte, error) {
	c.mu.Lock()
	c.commands = append(c.commands, method)
	var failure error
	var after bool
	if method == c.method {
		failure, after = c.failure, c.after
		c.method = ""
	}
	c.mu.Unlock()
	if failure != nil && !after {
		return nil, failure
	}
	data, err := c.Client.Call(ctx, session, method, params)
	if method == "Page.screencastFrameAck" && err == nil {
		select {
		case c.acked <- struct{}{}:
		default:
		}
	}
	return data, errors.Join(err, failure)
}

func (c *screencastTestClient) count(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, command := range c.commands {
		if command == method {
			count++
		}
	}
	return count
}

func newScreencastBrowser(t *testing.T) (*rod.Browser, *screencastTestClient) {
	t.Helper()
	goroutines.CheckLeak(t, 0, goroutines.Current(), goroutines.NonChildren())
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	t.Cleanup(cancel)
	l := launcher.New().Headless(true).RemoteDebuggingPort(0)
	t.Cleanup(func() {
		l.Kill()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := l.CleanupContext(cleanup); err != nil {
			t.Error(err)
		}
	})
	endpoint, err := l.LaunchNew(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := cdp.StartWithURL(ctx, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	client := &screencastTestClient{Client: transport, acked: make(chan struct{}, 64)}
	browser := rod.New().NoDefaultDevice().ControlURL("").Context(ctx).Client(client)
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := browser.CloseWithTimeout(5 * time.Second); err != nil {
			t.Error(err)
		}
	})
	return browser, client
}

func screencastPage(t *testing.T, browser *rod.Browser) *rod.Page {
	t.Helper()
	app, err := fixture.HTML(browser, `<!doctype html><body style="margin:0;background:white"><div id="box" style="position:absolute;left:10px;top:10px;width:30px;height:30px;background:red"></div>`, func(page *rod.Page) error {
		return page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: 200, Height: 100, DeviceScaleFactor: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := app.Page.WaitLoad(); err != nil {
		t.Fatal(err)
	}
	return app.Page
}

type screencastOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	failure  error
	closeErr error
	closes   int
}

func (w *screencastOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closes != 0 {
		return 0, io.ErrClosedPipe
	}
	if w.failure != nil {
		return 0, w.failure
	}
	return w.buffer.Write(data)
}

func (w *screencastOutput) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closes++
	return w.closeErr
}

func (w *screencastOutput) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.buffer.Bytes())
}

func startTestRecording(t *testing.T, page *rod.Page, stop <-chan struct{}, output io.WriteCloser) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(page.GetContext())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		owner := goroutines.Snapshot(false)[0].GoroutineID
		err := record(page.Context(ctx), stop, output)
		// Check recorder-owned work while the page and browser are still alive.
		if leak := goroutines.Check(0, func(trace *goroutines.Trace) bool { return !trace.HasParent(owner) }); leak != nil {
			t.Error(leak)
		}
		done <- err
	}()
	return done
}

func waitRecording(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(8 * time.Second):
		t.Fatal("record did not finish")
		return nil
	}
}

func waitScreencastAck(t *testing.T, client *screencastTestClient, done <-chan error) {
	t.Helper()
	select {
	case <-client.acked:
	case err := <-done:
		t.Fatalf("record ended before acknowledging a frame: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no screencast frame acknowledged")
	}
}

// An incomplete TAR is expected while the recorder is still writing it.
func recordedPosition(data []byte, x int) bool {
	archive := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := archive.Next()
		if err != nil {
			return false
		}
		if !strings.HasSuffix(header.Name, ".png") {
			continue
		}
		frame, err := png.Decode(archive)
		if err != nil {
			return false
		}
		red, green, blue, _ := frame.At(x+15, 25).RGBA()
		if frame.Bounds().Dx() == 200 && frame.Bounds().Dy() == 100 && red == 65535 && green == 0 && blue == 0 {
			return true
		}
	}
}

func waitRecordedPosition(t *testing.T, output *screencastOutput, x int, done <-chan error) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !recordedPosition(output.snapshot(), x) {
		select {
		case err := <-done:
			t.Fatalf("record ended before square at x=%d: %v", x, err)
		case <-timer.C:
			t.Fatalf("no PNG frame contains square at x=%d", x)
		case <-ticker.C:
		}
	}
}

func assertRecordingOutput(t *testing.T, output *screencastOutput, complete bool) {
	t.Helper()
	output.mu.Lock()
	closes := output.closes
	output.mu.Unlock()
	if closes != 1 {
		t.Fatalf("output closed %d times, want one", closes)
	}
	archive := tar.NewReader(bytes.NewReader(output.snapshot()))
	manifest := false
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if complete {
				t.Fatal(err)
			}
			break
		}
		if header.Name == "recording.ffconcat" {
			manifest = true
			data, err := io.ReadAll(archive)
			if err != nil || !bytes.HasPrefix(data, []byte("ffconcat version 1.0\n")) {
				t.Fatalf("manifest = %q, %v", data, err)
			}
		}
	}
	if manifest != complete {
		t.Fatalf("manifest present = %v, want %v", manifest, complete)
	}
}

func TestRecordFramesAndReuse(t *testing.T) {
	browser, client := newScreencastBrowser(t)
	page := screencastPage(t, browser)
	for range 2 {
		page.MustEval(`() => document.querySelector('#box').style.left = '10px'`)
		stop := make(chan struct{})
		output := new(screencastOutput)
		done := startTestRecording(t, page, stop, output)
		waitRecordedPosition(t, output, 10, done)
		page.MustEval(`() => document.querySelector('#box').style.left = '100px'`)
		waitRecordedPosition(t, output, 100, done)
		// A static final frame must remain visible until recording is stopped.
		time.Sleep(200 * time.Millisecond)
		close(stop)
		if err := waitRecording(t, done); err != nil {
			t.Fatal(err)
		}
		assertRecordingOutput(t, output, true)
		if !recordedPosition(output.snapshot(), 10) || !recordedPosition(output.snapshot(), 100) {
			t.Fatal("archive lost the initial or moved square")
		}
	}
	if starts, stops := client.count("Page.startScreencast"), client.count("Page.stopScreencast"); starts != 2 || stops != 2 {
		t.Fatalf("start/stop calls = %d/%d, want 2/2", starts, stops)
	}
}

func TestRecordCancellation(t *testing.T) {
	for _, mode := range []string{"idle", "deadline", "before-start"} {
		t.Run(mode, func(t *testing.T) {
			browser, client := newScreencastBrowser(t)
			page := screencastPage(t, browser)
			ctx, cancel := context.WithCancel(page.GetContext())
			defer cancel()
			want := context.Canceled
			if mode == "deadline" {
				var stopDeadline context.CancelFunc
				ctx, stopDeadline = context.WithTimeout(ctx, time.Second)
				defer stopDeadline()
				want = context.DeadlineExceeded
			} else if mode == "before-start" {
				cancel()
			}
			output := new(screencastOutput)
			done := startTestRecording(t, page.Context(ctx), make(chan struct{}), output)
			if mode != "before-start" {
				waitScreencastAck(t, client, done)
				if mode == "idle" {
					// The fixture remains static; cleanup cannot wait for another frame.
					cancel()
				}
			}
			if err := waitRecording(t, done); !errors.Is(err, want) {
				t.Fatalf("record error = %v, want %v", err, want)
			}
			assertRecordingOutput(t, output, false)
			page.MustEval(`() => document.querySelector('#box').textContent = 'still usable'`)
		})
	}
}

func TestRecordFailures(t *testing.T) {
	for _, mode := range []string{"start", "start-after", "ack", "write", "close", "page-close", "stop"} {
		t.Run(mode, func(t *testing.T) {
			browser, client := newScreencastBrowser(t)
			page := screencastPage(t, browser)
			failure := errors.New("injected " + mode + " failure")
			output := new(screencastOutput)
			switch mode {
			case "start", "start-after", "ack", "stop":
				methods := map[string]string{"start": "Page.startScreencast", "start-after": "Page.startScreencast", "ack": "Page.screencastFrameAck", "stop": "Page.stopScreencast"}
				client.mu.Lock()
				client.method, client.failure = methods[mode], failure
				client.after = mode == "start-after"
				client.mu.Unlock()
			case "write":
				output.failure = failure
			case "close":
				output.closeErr = failure
			}
			stop := make(chan struct{})
			done := startTestRecording(t, page, stop, output)
			if mode == "stop" || mode == "close" || mode == "page-close" {
				waitScreencastAck(t, client, done)
				if mode == "page-close" {
					if err := page.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					close(stop)
				}
			}
			err := waitRecording(t, done)
			if err == nil || (mode != "page-close" && !errors.Is(err, failure)) {
				t.Fatalf("record error = %v, want %v", err, failure)
			}
			assertRecordingOutput(t, output, mode == "close")
			// A closed session rejects Stop before reaching the transport.
			if got := client.count("Page.stopScreencast"); mode != "page-close" && got != 1 {
				t.Fatalf("stop calls = %d, want one", got)
			}
			if mode == "stop" || mode == "page-close" {
				if _, err := page.Eval(`() => 1`); err == nil {
					t.Fatal("discarded page is still usable")
				}
			} else {
				page.MustEval(`() => document.querySelector('#box').textContent = 'still usable'`)
			}
		})
	}
}

type listenerErrorCase struct {
	name string
	err  error
	want error
}

func TestListenerError(t *testing.T) {
	failure := errors.New("restore Page domain")
	for _, test := range []listenerErrorCase{
		{name: "success"},
		{name: "stopped", err: errListenerStopped},
		{name: "joined-stops", err: errors.Join(errListenerStopped, errListenerStopped)},
		{name: "restoration", err: failure, want: failure},
		{name: "joined-restoration", err: errors.Join(errListenerStopped, errors.Join(errListenerStopped, failure)), want: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := listenerError(test.err)
			if !errors.Is(err, test.want) || errors.Is(err, errListenerStopped) {
				t.Fatalf("listener error = %v, want %v without stop marker", err, test.want)
			}
		})
	}
}

type blockedScreencastOutput struct {
	entered chan struct{}
	closed  chan struct{}
	enter   sync.Once
	close   sync.Once
	closes  atomic.Int32
}

func (w *blockedScreencastOutput) Write([]byte) (int, error) {
	w.enter.Do(func() { close(w.entered) })
	<-w.closed
	return 0, io.ErrClosedPipe
}

func (w *blockedScreencastOutput) Close() error {
	w.closes.Add(1)
	w.close.Do(func() { close(w.closed) })
	return nil
}

func TestRecordBlockedOutput(t *testing.T) {
	for _, mode := range []string{"cancel", "stop", "queue-full"} {
		t.Run(mode, func(t *testing.T) {
			browser, client := newScreencastBrowser(t)
			page := screencastPage(t, browser)
			ctx, cancel := context.WithCancel(page.GetContext())
			defer cancel()
			output := &blockedScreencastOutput{entered: make(chan struct{}), closed: make(chan struct{})}
			stop := make(chan struct{})
			done := startTestRecording(t, page.Context(ctx), stop, output)
			select {
			case <-output.entered:
			case err := <-done:
				t.Fatalf("record ended before writing: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("record did not start writing")
			}
			waitScreencastAck(t, client, done)
			var err error
			finished := false
			switch mode {
			case "cancel":
				cancel()
			case "stop":
				close(stop)
			case "queue-full":
				// Each paint is acknowledged even while the first output write is blocked.
				for x := 20; x <= 110 && !finished; x += 10 {
					page.MustEval(`x => document.querySelector('#box').style.left = x + 'px'`, x)
					select {
					case <-client.acked:
					case err = <-done:
						finished = true
					case <-time.After(5 * time.Second):
						t.Fatal("blocked output prevented frame acknowledgement")
					}
				}
			}
			if !finished {
				err = waitRecording(t, done)
			}
			if err == nil {
				t.Fatal("blocked output completed successfully")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("record error = %v, want cancellation", err)
			}
			if mode == "queue-full" && !errors.Is(err, errSlowOutput) {
				t.Fatalf("record error = %v, want full queue", err)
			}
			if output.closes.Load() != 1 || client.count("Page.stopScreencast") != 1 {
				t.Fatal("blocked output did not close and stop exactly once")
			}
			page.MustEval(`() => document.querySelector('#box').textContent = 'still usable'`)
		})
	}
}
