package rod

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

// ErrDiagnosticsIncomplete means the protocol boundary or cleanup could not finish.
// The returned snapshot still contains the diagnostics collected so far.
var ErrDiagnosticsIncomplete = errors.New("rod: incomplete page diagnostics")

// ErrDiagnosticsOptions means a diagnostics limit is negative.
var ErrDiagnosticsOptions = errors.New("rod: invalid diagnostics options")

// DiagnosticsOptions bounds collection. Zero fields select the documented defaults.
type DiagnosticsOptions struct {
	// MaxRecords retains the first 1000 records of each kind by default.
	MaxRecords int
	// MaxTextBytes bounds each retained text field, including rendered arguments.
	// The default is 4096 bytes; truncated text ends with an ellipsis.
	MaxTextBytes int
	// MaxRequests bounds in-flight request tracking (default 1000). New requests
	// beyond the limit are not tracked until a slot becomes available.
	MaxRequests int
	// MaxPreviewDepth bounds nested object previews (default 3).
	MaxPreviewDepth int
	// StopTimeout bounds draining and, separately, cleanup (default 5 seconds).
	StopTimeout time.Duration
}

// DiagnosticLocation uses one-based coordinates; zero means unavailable.
type DiagnosticLocation struct {
	URL    string
	Line   int
	Column int
}

// ConsoleMessage is rendered only from the console event payload.
type ConsoleMessage struct {
	Type proto.RuntimeConsoleAPICalledType
	Text string
	DiagnosticLocation
}

// PageError is an unhandled exception that has not subsequently been revoked.
type PageError struct {
	ExceptionID int
	Text        string
	DiagnosticLocation
}

// ResourceFailure correlates an HTTP error and any subsequent loading failure.
// URL can be empty when no request or response was observed for RequestID.
type ResourceFailure struct {
	RequestID     proto.NetworkRequestID
	URL           string
	Type          proto.NetworkResourceType
	Status        int
	ErrorText     string
	BlockedReason proto.NetworkBlockedReason
	Canceled      bool
}

// DiagnosticsSnapshot owns its slices and is safe to modify. Each category keeps
// its first MaxRecords entries; Dropped counts later entries. Revoked exceptions
// free their retained slot. DroppedRequests counts denied tracking admissions.
type DiagnosticsSnapshot struct {
	Console                 []ConsoleMessage
	PageErrors              []PageError
	ResourceFailures        []ResourceFailure
	DroppedConsole          int
	DroppedPageErrors       int
	DroppedResourceFailures int
	DroppedRequests         int
}

type diagnosticRequest struct {
	url            string
	typ            proto.NetworkResourceType
	status         int
	failureDropped bool
}

// PageDiagnostics collects events delivered to one page's CDP session, including
// its same-process frames. It does not attach to workers, popups or cross-process
// frames. Snapshot and Stop may be called concurrently.
type PageDiagnostics struct {
	page       *Page
	options    DiagnosticsOptions
	cancel     context.CancelFunc
	done       chan struct{}
	marker     string
	worldName  string
	stopOnce   sync.Once
	mu         sync.Mutex
	data       DiagnosticsSnapshot
	requests   map[[sha256.Size]byte]diagnosticRequest
	failureIDs [][sha256.Size]byte
	complete   bool
	err        error
}

// StartDiagnostics subscribes before enabling Runtime and Network, and returns
// only after both enables succeed. Start before navigation to capture initial
// scripts. Setup errors release subscriptions and acquired domain ownership.
func (p *Page) StartDiagnostics(options DiagnosticsOptions) (*PageDiagnostics, error) {
	if options.MaxRecords < 0 || options.MaxTextBytes < 0 || options.MaxRequests < 0 || options.MaxPreviewDepth < 0 || options.StopTimeout < 0 {
		return nil, ErrDiagnosticsOptions
	}
	if options.MaxRecords == 0 {
		options.MaxRecords = 1000
	}
	if options.MaxTextBytes == 0 {
		options.MaxTextBytes = 4096
	}
	if options.MaxRequests == 0 {
		options.MaxRequests = 1000
	}
	if options.MaxPreviewDepth == 0 {
		options.MaxPreviewDepth = 3
	}
	if options.StopTimeout == 0 {
		options.StopTimeout = 5 * time.Second
	}
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	d := &PageDiagnostics{
		page: p, options: options, cancel: cancel, done: make(chan struct{}),
		marker:    "__rod_diagnostics_" + rand.Text(),
		worldName: fmt.Sprintf("__rod_diagnostics_%x", sha256.Sum256([]byte(p.FrameID))),
		requests:  make(map[[sha256.Size]byte]diagnosticRequest),
	}
	messages := p.browser.event.Subscribe(ctx)
	var releases []func(context.Context) error
	cleanup := func() error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), options.StopTimeout)
		defer cancel()
		var err error
		for _, release := range slices.Backward(releases) {
			err = errors.Join(err, release(ctx))
		}
		return err
	}
	for _, req := range []proto.Request{&proto.RuntimeEnable{}, &proto.NetworkEnable{}} {
		release, err := p.browser.Context(ctx).acquireDomain(p.SessionID, req)
		if err != nil {
			cancel()
			return nil, errors.Join(fmt.Errorf("rod: start diagnostics: %w", err), cleanup())
		}
		releases = append(releases, release)
	}
	go func() {
		defer close(d.done)
		for msg := range messages {
			if msg.SessionID != p.SessionID {
				continue
			}
			if msg.Method == (proto.RuntimeBindingCalled{}).ProtoEvent() {
				var event proto.RuntimeBindingCalled
				msg.Load(&event)
				if event.Name == d.marker && event.Payload == d.marker {
					d.mu.Lock()
					d.complete = true
					d.mu.Unlock()
					break
				}
			}
			d.collect(msg)
		}
		cancel()
		cleanupErr := cleanup()
		d.mu.Lock()
		defer d.mu.Unlock()
		if !d.complete {
			d.err = errors.Join(d.err, ErrDiagnosticsIncomplete, ctx.Err())
		}
		if cleanupErr != nil {
			d.err = errors.Join(d.err, ErrDiagnosticsIncomplete, cleanupErr)
		}
		clear(d.requests)
	}()
	return d, nil
}

// Snapshot copies all currently processed records. It does not drain the event
// queue. Console previews are abbreviated payload representations, not full
// object snapshots or Chrome DevTools formatting; getters are never invoked.
func (d *PageDiagnostics) Snapshot() DiagnosticsSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	data := d.data
	data.Console = slices.Clone(data.Console)
	data.PageErrors = slices.Clone(data.PageErrors)
	data.ResourceFailures = slices.Clone(data.ResourceFailures)
	return data
}

// Stop is idempotent. It processes events through a private Runtime binding call
// in an isolated world, then releases its subscriptions and domain ownership.
// This protocol boundary does not wait for future timers, promises or requests.
// Cancellation or target closure returns the retained data and an error matching
// ErrDiagnosticsIncomplete. Draining and cleanup each have a StopTimeout budget.
func (d *PageDiagnostics) Stop() (DiagnosticsSnapshot, error) {
	d.stopOnce.Do(func() {
		select {
		case <-d.done:
			return
		default:
		}
		ctx, cancel := context.WithTimeout(d.page.ctx, d.options.StopTimeout)
		defer cancel()
		world, err := proto.PageCreateIsolatedWorld{FrameID: d.page.FrameID, WorldName: d.worldName}.Call(d.client(ctx))
		bindingAttempted := false
		if err == nil {
			bindingAttempted = true
			err = proto.RuntimeAddBinding{Name: d.marker, ExecutionContextName: d.worldName}.Call(d.client(ctx))
		}
		if err == nil {
			var result *proto.RuntimeEvaluateResult
			result, err = (proto.RuntimeEvaluate{
				ContextID: world.ExecutionContextID, Silent: new(true),
				Expression: "globalThis[" + strconv.Quote(d.marker) + "](" + strconv.Quote(d.marker) + "); delete globalThis[" + strconv.Quote(d.marker) + "]",
			}).Call(d.client(ctx))
			if err == nil && result.ExceptionDetails != nil {
				err = fmt.Errorf("rod: diagnostics boundary: %s", result.ExceptionDetails.Text)
			}
		}
		if err == nil {
			select {
			case <-d.done:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		d.cancel()
		// Cleanup uses an independent context even if the page operation expired.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(d.page.ctx), d.options.StopTimeout)
		defer cleanupCancel()
		if bindingAttempted {
			// removeBinding stops notifications but does not delete the JS function.
			_, deleteErr := (proto.RuntimeEvaluate{ContextID: world.ExecutionContextID, Silent: new(true), Expression: "delete globalThis[" + strconv.Quote(d.marker) + "]"}).Call(d.client(cleanupCtx))
			err = errors.Join(err, deleteErr, proto.RuntimeRemoveBinding{Name: d.marker}.Call(d.client(cleanupCtx)))
		}
		select {
		case <-d.done:
		case <-cleanupCtx.Done():
			err = errors.Join(err, cleanupCtx.Err())
		}
		if err != nil {
			d.mu.Lock()
			d.err = errors.Join(d.err, ErrDiagnosticsIncomplete, err)
			d.mu.Unlock()
		}
	})
	d.mu.Lock()
	err := d.err
	d.mu.Unlock()
	return d.Snapshot(), err
}

func (d *PageDiagnostics) client(ctx context.Context) *Page {
	return d.page.Context(ctx)
}
