package rod

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

type stateKey struct {
	browserContextID proto.BrowserBrowserContextID
	sessionID        proto.TargetSessionID
	methodName       string
}

func (b *Browser) key(sessionID proto.TargetSessionID, methodName string) stateKey {
	contextID := proto.BrowserBrowserContextID("")
	if req := proto.GetType(methodName); req != nil {
		if _, scoped := req.FieldByName("BrowserContextID"); scoped {
			contextID = b.BrowserContextID
		}
	}
	return stateKey{
		browserContextID: contextID,
		sessionID:        sessionID,
		methodName:       methodName,
	}
}

func (b *Browser) set(sessionID proto.TargetSessionID, methodName string, params any) {
	b.setState(sessionID, methodName, params, b.sessionContext(sessionID))
}

func (b *Browser) setState(sessionID proto.TargetSessionID, methodName string, params any, sessionCtx context.Context) {
	b.stateLock.Lock()
	defer b.stateLock.Unlock()
	if sessionCtx != nil && sessionCtx.Err() != nil {
		return
	}
	if b.connectionCtx != nil && b.connectionCtx.Err() != nil {
		return
	}
	state := b.key(sessionID, methodName)
	switch req := params.(type) {
	case proto.BrowserSetDownloadBehavior:
		state.browserContextID = req.BrowserContextID
		b.states.Store(downloadEventsEnabledKey{}, req.EventsEnabled != nil && *req.EventsEnabled)
	case *proto.BrowserSetDownloadBehavior:
		state.browserContextID = req.BrowserContextID
		b.states.Store(downloadEventsEnabledKey{}, req.EventsEnabled != nil && *req.EventsEnabled)
	}
	b.states.Store(state, params)

	key := ""
	switch methodName {
	case (proto.EmulationClearDeviceMetricsOverride{}).ProtoReq():
		key = (proto.EmulationSetDeviceMetricsOverride{}).ProtoReq()
	case (proto.EmulationClearGeolocationOverride{}).ProtoReq():
		key = (proto.EmulationSetGeolocationOverride{}).ProtoReq()
	default:
		domain, name := proto.ParseMethodName(methodName)
		if name == "disable" {
			key = domain + ".enable"
		}
	}
	if key != "" {
		b.states.Delete(b.key(sessionID, key))
	}
}

// LoadState into the method, sessionID can be empty.
func (b *Browser) LoadState(sessionID proto.TargetSessionID, method proto.Request) (has bool) {
	data, has := b.states.Load(b.key(sessionID, method.ProtoReq()))
	if has {
		reflect.Indirect(reflect.ValueOf(method)).Set(
			reflect.Indirect(reflect.ValueOf(data)),
		)
	}
	return
}

// RemoveState a state.
func (b *Browser) RemoveState(key any) {
	b.states.Delete(key)
}

type domainLeaseKey struct{ stateKey }

type domainLease struct {
	mu      sync.Mutex
	owners  int
	owned   bool
	changed chan struct{} // non-nil while a protocol transition is in progress
}

// acquireDomain shares automatic enablement across listeners on this connection.
// The lease lives alongside the connection state so Browser context clones share it.
func (b *Browser) acquireDomain(sessionID proto.TargetSessionID, req proto.Request) (func(context.Context) error, error) {
	key := b.key(sessionID, req.ProtoReq())
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
	value, _ := b.states.LoadOrStore(domainLeaseKey{key}, new(domainLease))
	b.stateLock.Unlock()
	lease := value.(*domainLease)
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
		enabled := b.domainEnabled(key)
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
		_, err := b.Call(b.ctx, string(sessionID), req.ProtoReq(), req)
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

func (b *Browser) domainEnabled(key stateKey) bool {
	value, enabled := b.states.Load(key)
	switch req := value.(type) {
	case proto.PageSetLifecycleEventsEnabled:
		return req.Enabled
	case *proto.PageSetLifecycleEventsEnabled:
		return req.Enabled
	}
	return enabled
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
// Restoration uses an independent five-second cleanup budget.
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
func (b *Browser) DisableDomain(sessionID proto.TargetSessionID, req proto.Request) (func() error, error) {
	sessionCtx := b.sessionContext(sessionID)
	ctx, cancel := contextWithSession(b.ctx, sessionCtx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := b.key(sessionID, req.ProtoReq())
	enabled := b.domainEnabled(key)
	previous, _ := b.states.Load(key)
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
			_, restoreErr = b.Call(ctx, string(sessionID), req.ProtoReq(), previous)
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

// LoadState into the method.
func (p *Page) LoadState(method proto.Request) (has bool) {
	return p.browser.LoadState(p.SessionID, method)
}

// EnableDomain shares enablement until its idempotent restore function is called.
// Setup and bounded restoration errors are returned to the caller.
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
	b.states.Range(func(key, _ any) bool {
		switch key := key.(type) {
		case stateKey:
			if key.sessionID == p.SessionID {
				b.states.Delete(key)
			}
		case domainLeaseKey:
			if key.sessionID == p.SessionID {
				b.states.Delete(key)
			}
		}
		return true
	})
}
