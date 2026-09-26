package rod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

// ErrUnsupportedDomain means a domain helper received a request other than a
// domain enable command or Page.setLifecycleEventsEnabled.
var ErrUnsupportedDomain = errors.New("rod: domain helpers require a domain enable command or Page.setLifecycleEventsEnabled")

// retainedCommands holds the retained commands and domain leases of one session,
// or the context-scoped commands of one browser context. Browser.stateLock
// guards its maps.
type retainedCommands struct {
	commands map[string]json.RawMessage
	leases   map[string]*domainLease
}

// sessionCommandsKey and contextCommandsKey index retainedCommands in Browser.states.
type sessionCommandsKey proto.TargetSessionID
type contextCommandsKey proto.BrowserBrowserContextID

// retainedScope reports whether a successful command's parameters are retained,
// and whether they belong to a browser context rather than a session.
func retainedScope(method string) (retained, contextScoped bool) {
	switch method {
	case (proto.BrowserSetDownloadBehavior{}).ProtoReq():
		return true, true
	case (proto.PageSetLifecycleEventsEnabled{}).ProtoReq(),
		(proto.PageSetInterceptFileChooserDialog{}).ProtoReq(),
		(proto.EmulationSetDeviceMetricsOverride{}).ProtoReq():
		return true, false
	}
	return strings.HasSuffix(method, ".enable"), false
}

// clearedCommand returns the retained command that a successful command removes.
func clearedCommand(method string) string {
	if method == (proto.EmulationClearDeviceMetricsOverride{}).ProtoReq() {
		return (proto.EmulationSetDeviceMetricsOverride{}).ProtoReq()
	}
	if domain, disable := strings.CutSuffix(method, ".disable"); disable {
		return domain + ".enable"
	}
	return ""
}

// decodeRetained decodes retained parameters as the method's protocol type.
// Unknown methods and parameters that do not decode stay encoded.
func decodeRetained(method string, data json.RawMessage) any {
	typ := proto.GetType(method)
	if typ == nil {
		return data
	}
	value := reflect.New(typ)
	if json.Unmarshal(data, value.Interface()) != nil {
		return data
	}
	return value.Elem().Interface()
}

// retained returns the commands stored under key, creating them when requested.
// The caller holds stateLock.
func (b *Browser) retained(key any, create bool) *retainedCommands {
	if value, ok := b.states.Load(key); ok {
		return value.(*retainedCommands)
	}
	if !create {
		return nil
	}
	commands := &retainedCommands{commands: make(map[string]json.RawMessage)}
	b.states.Store(key, commands)
	return commands
}

func (b *Browser) retainedKey(sessionID proto.TargetSessionID, contextScoped bool) any {
	if contextScoped {
		return contextCommandsKey(b.BrowserContextID)
	}
	return sessionCommandsKey(sessionID)
}

func (b *Browser) set(sessionID proto.TargetSessionID, methodName string, params any) {
	b.setState(sessionID, methodName, params, b.sessionContext(sessionID))
}

// setState records a successful command. Commands that neither set nor remove
// retained state return without locking. Retained parameters are stored in
// their protocol encoding, so neither the caller's request nor a loaded copy
// shares memory with them.
func (b *Browser) setState(sessionID proto.TargetSessionID, methodName string, params any, sessionCtx context.Context) {
	retained, _ := retainedScope(methodName)
	cleared := clearedCommand(methodName)
	disposed := methodName == (proto.TargetDisposeBrowserContext{}).ProtoReq()
	if !retained && cleared == "" && !disposed {
		return
	}
	var data json.RawMessage
	if retained || disposed {
		var err error
		if data, err = json.Marshal(params); err != nil {
			return
		}
	}
	key := any(sessionCommandsKey(sessionID))
	var download proto.BrowserSetDownloadBehavior
	switch methodName {
	case (proto.TargetDisposeBrowserContext{}).ProtoReq():
		var req proto.TargetDisposeBrowserContext
		if json.Unmarshal(data, &req) != nil {
			return
		}
		key = contextCommandsKey(req.BrowserContextID)
	case download.ProtoReq():
		if json.Unmarshal(data, &download) != nil {
			return
		}
		// The request's context, rather than the caller's, owns the setting.
		key = contextCommandsKey(download.BrowserContextID)
	}
	b.stateLock.Lock()
	defer b.stateLock.Unlock()
	if disposed {
		// Settings of a disposed context cannot apply again.
		b.states.Delete(key)
		return
	}
	if sessionCtx != nil && sessionCtx.Err() != nil {
		return
	}
	if b.connectionCtx != nil && b.connectionCtx.Err() != nil {
		return
	}
	if cleared != "" {
		if commands := b.retained(key, false); commands != nil {
			delete(commands.commands, cleared)
		}
		return
	}
	if methodName == download.ProtoReq() {
		b.states.Store(downloadEventsEnabledKey{}, download.EventsEnabled != nil && *download.EventsEnabled)
	}
	b.retained(key, true).commands[methodName] = data
}

func (b *Browser) loadState(sessionID proto.TargetSessionID, method string) (json.RawMessage, bool) {
	retained, contextScoped := retainedScope(method)
	if !retained {
		return nil, false
	}
	b.stateLock.Lock()
	defer b.stateLock.Unlock()
	commands := b.retained(b.retainedKey(sessionID, contextScoped), false)
	if commands == nil {
		return nil, false
	}
	data, has := commands.commands[method]
	return data, has
}

// LoadState reports whether the last successful command of the method is
// retained. When method is a pointer, LoadState also replaces the value it
// points to with a copy decoded from the parameters that were sent. Only these
// commands are retained:
//
//   - Domain enable commands, such as Network.enable, until the domain's
//     disable command.
//   - Page.setLifecycleEventsEnabled and Page.setInterceptFileChooserDialog.
//   - Emulation.setDeviceMetricsOverride, until
//     Emulation.clearDeviceMetricsOverride.
//   - Browser.setDownloadBehavior, for each browser context. LoadState reads the
//     setting of this Browser's context and ignores sessionID. It is removed when
//     Target.disposeBrowserContext disposes the context.
//
// Other commands are not retained. The retained commands of a session are
// removed when the session detaches, and all retained commands are removed when
// the browser connection ends. Use an empty sessionID for browser-level commands.
func (b *Browser) LoadState(sessionID proto.TargetSessionID, method proto.Request) (has bool) {
	data, has := b.loadState(sessionID, method.ProtoReq())
	if !has {
		return false
	}
	target := reflect.ValueOf(method)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return true
	}
	// Decode into a new value so fields the command omitted are reset.
	value := reflect.New(target.Type().Elem())
	if json.Unmarshal(data, value.Interface()) == nil {
		target.Elem().Set(value.Elem())
	}
	return true
}

// releaseDetachedSession removes the retained commands and domain leases of a
// session when Target.detachedFromTarget reports that it ended, including
// sessions that PageFromTarget did not attach.
func (b *Browser) releaseDetachedSession(method string, params json.RawMessage) {
	if method != (proto.TargetDetachedFromTarget{}).ProtoEvent() {
		return
	}
	var detached proto.TargetDetachedFromTarget
	if b.decoding.Unmarshal(params, &detached) != nil || detached.SessionID == "" {
		return
	}
	b.stateLock.Lock()
	defer b.stateLock.Unlock()
	b.states.Delete(sessionCommandsKey(detached.SessionID))
}

// RemoveState a state.
func (b *Browser) RemoveState(key any) {
	b.states.Delete(key)
}

// domainRequest rejects requests whose enabled state the domain helpers cannot track.
func domainRequest(req proto.Request) error {
	method := req.ProtoReq()
	if method == (proto.PageSetLifecycleEventsEnabled{}).ProtoReq() || strings.HasSuffix(method, ".enable") {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedDomain, method)
}

type domainLease struct {
	mu      sync.Mutex
	owners  int
	owned   bool
	changed chan struct{} // non-nil while a protocol transition is in progress
}

// acquireDomain shares automatic enablement across listeners on this connection.
// The lease lives alongside the connection state so Browser context clones share it.
func (b *Browser) acquireDomain(sessionID proto.TargetSessionID, req proto.Request) (func(context.Context) error, error) {
	if err := domainRequest(req); err != nil {
		return nil, err
	}
	method := req.ProtoReq()
	sessionCtx := b.sessionContext(sessionID)
	ctx, cancel := contextWithSession(b.ctx, sessionCtx)
	defer cancel()
	b = b.Context(ctx)
	b.stateLock.Lock()
	if b.connectionCtx != nil && b.connectionCtx.Err() != nil {
		b.stateLock.Unlock()
		return nil, b.connectionCtx.Err()
	}
	if err := b.ctx.Err(); err != nil {
		b.stateLock.Unlock()
		return nil, err
	}
	if sessionCtx != nil && sessionCtx.Err() != nil {
		b.stateLock.Unlock()
		return nil, sessionCtx.Err()
	}
	commands := b.retained(sessionCommandsKey(sessionID), true)
	lease := commands.leases[method]
	if lease == nil {
		if commands.leases == nil {
			commands.leases = make(map[string]*domainLease)
		}
		lease = new(domainLease)
		commands.leases[method] = lease
	}
	b.stateLock.Unlock()
	for {
		if err := b.ctx.Err(); err != nil {
			return nil, err
		}
		lease.mu.Lock()
		if changed := lease.changed; changed != nil {
			lease.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-b.ctx.Done():
				return nil, b.ctx.Err()
			}
		}
		_, enabled := b.domainState(sessionID, method)
		if lease.owners > 0 {
			lease.owners++
			lease.mu.Unlock()
			break
		}
		lease.owned = !enabled
		if enabled {
			lease.owners = 1
			lease.mu.Unlock()
			break
		}
		lease.changed = make(chan struct{})
		lease.mu.Unlock()
		_, err := b.Call(b.ctx, string(sessionID), method, req)
		if err != nil {
			// The browser can apply enable before a canceled request loses its reply.
			// Roll back uncertain setup while this transition still excludes new owners.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
			cleanupErr := b.disableAutomaticDomain(ctx, sessionID, req)
			cancel()
			err = errors.Join(err, cleanupErr)
		}
		lease.mu.Lock()
		if err == nil {
			lease.owners++
		}
		close(lease.changed)
		lease.changed = nil
		lease.mu.Unlock()
		if err != nil {
			return nil, err
		}
		break
	}
	var once sync.Once
	var err error
	return func(ctx context.Context) error {
		once.Do(func() {
			ctx, cancel := contextWithSession(ctx, sessionCtx)
			defer cancel()
			lease.mu.Lock()
			lease.owners--
			if lease.owners != 0 || !lease.owned {
				lease.mu.Unlock()
				return
			}
			lease.changed = make(chan struct{})
			lease.mu.Unlock()
			err = b.disableAutomaticDomain(ctx, sessionID, req)
			lease.mu.Lock()
			close(lease.changed)
			lease.changed = nil
			lease.mu.Unlock()
		})
		return err
	}, nil
}

// domainState returns the retained enable command of a domain and whether it
// leaves the domain enabled.
func (b *Browser) domainState(sessionID proto.TargetSessionID, method string) (json.RawMessage, bool) {
	data, enabled := b.loadState(sessionID, method)
	if enabled && method == (proto.PageSetLifecycleEventsEnabled{}).ProtoReq() {
		var req proto.PageSetLifecycleEventsEnabled
		enabled = json.Unmarshal(data, &req) == nil && req.Enabled
	}
	return data, enabled
}

func (b *Browser) disableAutomaticDomain(ctx context.Context, session proto.TargetSessionID, req proto.Request) error {
	if req.ProtoReq() == (proto.PageSetLifecycleEventsEnabled{}).ProtoReq() {
		_, err := b.Call(ctx, string(session), req.ProtoReq(), proto.PageSetLifecycleEventsEnabled{Enabled: false})
		return err
	}
	domain, _ := proto.ParseMethodName(req.ProtoReq())
	_, err := b.Call(ctx, string(session), domain+".disable", nil)
	return err
}

// EnableDomain enables a domain until its idempotent restore function is called.
// Overlapping users share ownership; the last restores the original state.
// Domains explicitly enabled before the first owner remain enabled.
// Restoration uses an independent five-second cleanup budget. req is a domain
// enable command or Page.setLifecycleEventsEnabled; other requests return
// [ErrUnsupportedDomain].
func (b *Browser) EnableDomain(sessionID proto.TargetSessionID, req proto.Request) (func() error, error) {
	release, err := b.acquireDomain(sessionID, req)
	if err != nil {
		return nil, err
	}
	return func() error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
		defer cancel()
		return release(ctx)
	}, nil
}

// DisableDomain temporarily disables a domain, including active automatic owners.
// Its idempotent restore function reapplies the previous enabled configuration
// using an independent five-second cleanup budget. Failed setup attempts restore
// that configuration before returning the setup and any restoration errors.
// Requests follow the [Browser.EnableDomain] rules.
func (b *Browser) DisableDomain(sessionID proto.TargetSessionID, req proto.Request) (func() error, error) {
	if err := domainRequest(req); err != nil {
		return nil, err
	}
	sessionCtx := b.sessionContext(sessionID)
	ctx, cancel := contextWithSession(b.ctx, sessionCtx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	previous, enabled := b.domainState(sessionID, req.ProtoReq())
	var once sync.Once
	var restoreErr error
	restore := func() error {
		once.Do(func() {
			if !enabled {
				return
			}
			ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
			defer cancel()
			ctx, stopSession := contextWithSession(ctx, sessionCtx)
			defer stopSession()
			_, restoreErr = b.Call(ctx, string(sessionID), req.ProtoReq(), decodeRetained(req.ProtoReq(), previous))
		})
		return restoreErr
	}
	if enabled {
		if err := b.disableAutomaticDomain(ctx, sessionID, req); err != nil {
			return nil, errors.Join(err, restore())
		}
	}
	return restore, nil
}

type sessionStateKey proto.TargetSessionID

func (b *Browser) sessionContext(id proto.TargetSessionID) context.Context {
	if ctx, ok := b.states.Load(sessionStateKey(id)); ok {
		return ctx.(context.Context)
	}
	return nil
}

func (b *Browser) connectionContext() context.Context {
	if b.connectionCtx != nil {
		return b.connectionCtx
	}
	return b.ctx
}

func (b *Browser) cachePage(page *Page) {
	b.stateLock.Lock()
	defer b.stateLock.Unlock()
	if page.sessionCtx == nil || page.sessionCtx.Err() == nil {
		b.states.Store(page.TargetID, page)
	}
}

func (b *Browser) loadCachedPage(id proto.TargetTargetID) *Page {
	if cache, ok := b.states.Load(id); ok {
		page := cache.(*Page)
		if page.sessionCtx == nil || page.sessionCtx.Err() == nil {
			return page
		}
	}
	return nil
}

// LoadState loads the page session's retained command into method, following
// the [Browser.LoadState] rules.
func (p *Page) LoadState(method proto.Request) (has bool) {
	return p.browser.LoadState(p.SessionID, method)
}

// EnableDomain shares enablement until its idempotent restore function is called.
// Setup and bounded restoration errors are returned to the caller. Requests
// follow the [Browser.EnableDomain] rules.
func (p *Page) EnableDomain(method proto.Request) (func() error, error) {
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	defer cancel()
	return p.browser.Context(ctx).EnableDomain(p.SessionID, method)
}

// DisableDomain temporarily disables a domain and returns an idempotent function
// that restores its previous configuration, including after caller cancellation.
func (p *Page) DisableDomain(method proto.Request) (func() error, error) {
	ctx, cancel := contextWithSession(p.ctx, p.sessionCtx)
	defer cancel()
	return p.browser.Context(ctx).DisableDomain(p.SessionID, method)
}

// terminateSession prevents completed requests from repopulating state, then
// drops every retained command and domain lease belonging to this attachment.
func (p *Page) terminateSession() {
	b := p.browser
	b.stateLock.Lock()
	defer b.stateLock.Unlock()
	if p.sessionCancel != nil {
		p.sessionCancel()
	}
	b.states.CompareAndDelete(p.TargetID, p.root)
	if p.root == nil {
		b.states.CompareAndDelete(p.TargetID, p)
	}
	b.states.Delete(sessionStateKey(p.SessionID))
	if p.SessionID != "" { // Browser-level commands outlive page views.
		b.states.Delete(sessionCommandsKey(p.SessionID))
	}
}
