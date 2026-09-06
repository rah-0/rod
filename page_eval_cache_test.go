package rod

import (
	"sync"
	"testing"

	"github.com/rah-0/rod/lib/proto"
)

func TestJSHelperCacheInvalidationAcrossViews(t *testing.T) {
	for _, clearAll := range []bool{false, true} {
		page := &Page{
			ctx: t.Context(), jsCtxLock: &sync.Mutex{}, jsCtxID: new(proto.RuntimeRemoteObjectID("old")),
			helpers: &jsHelperCache{contexts: map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{
				"old": {"element": "old-function"},
			}},
		}
		view := page.Context(t.Context())
		if id, ok := view.getHelper("old", "element"); !ok || id != "old-function" {
			t.Fatal("view did not share the original helper")
		}
		page.helpers.Lock()
		if clearAll {
			page.helpers.contexts = nil
		} else {
			delete(page.helpers.contexts, "old")
		}
		page.helpers.Unlock()
		if view.setHelper("old", "element", "stale-function") {
			t.Fatal("stale helper was accepted after invalidation")
		}
		if _, ok := view.getHelper("old", "element"); ok {
			t.Fatal("view retained a helper after invalidation")
		}
		if len(page.helpers.contexts) != 0 {
			t.Fatal("lookup recreated an invalidated context")
		}
		page.helpers.Lock()
		page.helpers.contexts = map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{"current": {}}
		page.helpers.Unlock()
		if !view.setHelper("current", "element", "current-function") {
			t.Fatal("current helper was not cached")
		}
		if id, ok := page.getHelper("current", "element"); !ok || id != "current-function" {
			t.Fatal("new cache was not visible through the original page")
		}
	}
}

func TestJSHelperCacheUnsetInvalidatesSharedContext(t *testing.T) {
	page := &Page{
		ctx: t.Context(), jsCtxLock: &sync.Mutex{}, jsCtxID: new(proto.RuntimeRemoteObjectID("old")),
		helpers: &jsHelperCache{contexts: map[proto.RuntimeRemoteObjectID]map[string]proto.RuntimeRemoteObjectID{"old": {}}},
	}
	view := page.Context(t.Context())
	view.unsetJSCtxID()
	if *page.jsCtxID != "" || page.setHelper("old", "element", "stale") {
		t.Fatal("reset did not invalidate the context and its helpers in every view")
	}
}
