package rod_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

// searchResetClient is an endpoint whose sessions number their DOM nodes as
// Chrome does. A search returns node ID 0 until DOM.getDocument has sent the
// document of the session. DOM.getDocument gives the node a new ID and discards
// the search results of the session, as DOM.disable does.
type searchResetClient struct {
	events chan *cdp.Event
	// before, when it is set, runs before a command takes effect.
	before func(ctx context.Context, session, method string) error

	mu       sync.Mutex
	answered *sync.Cond
	sessions map[string]*searchResetSession
	// calls counts the answered commands of each session by method.
	calls map[searchResetCall]int
}

type searchResetCall struct{ session, method string }

type searchResetSession struct {
	// documents is the ID of the node while sent is set.
	documents int
	sent      bool
	searches  map[string]bool
	searchID  int
}

func (c *searchResetClient) Event() <-chan *cdp.Event { return c.events }

func (c *searchResetClient) Call(ctx context.Context, session, method string, params any) ([]byte, error) {
	if c.before != nil {
		if err := c.before(ctx, session, method); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.answered.Broadcast()
	c.calls[searchResetCall{session, method}]++
	s := c.sessions[session]
	if s == nil {
		s = &searchResetSession{searches: map[string]bool{}}
		c.sessions[session] = s
	}
	switch method {
	case "Target.attachToTarget":
		return json.Marshal(proto.TargetAttachToTargetResult{SessionID: proto.TargetSessionID(params.(proto.TargetAttachToTarget).TargetID)})
	case "Runtime.evaluate":
		return []byte(`{"result":{"type":"object","objectId":"window"}}`), nil
	case "Runtime.callFunctionOn":
		return []byte(`{"result":{"type":"undefined"}}`), nil
	case "DOM.disable":
		s.sent = false
		clear(s.searches)
	case "DOM.getDocument":
		s.documents++
		s.sent = true
		clear(s.searches)
		return []byte(`{"root":{"nodeId":1,"backendNodeId":1,"nodeType":9,"nodeName":"#document","localName":"","nodeValue":""}}`), nil
	case "DOM.performSearch":
		s.searchID++
		id := fmt.Sprint("search-", s.searchID)
		s.searches[id] = true
		return json.Marshal(proto.DOMPerformSearchResult{SearchID: id, ResultCount: 1})
	case "DOM.getSearchResults":
		if !s.searches[params.(proto.DOMGetSearchResults).SearchID] {
			return nil, cdp.ErrSearchSessionNotFound
		}
		if !s.sent {
			return []byte(`{"nodeIds":[0]}`), nil
		}
		return json.Marshal(proto.DOMGetSearchResultsResult{NodeIDs: []proto.DOMNodeID{proto.DOMNodeID(s.documents)}})
	case "DOM.discardSearchResults":
		delete(s.searches, params.(proto.DOMDiscardSearchResults).SearchID)
	case "DOM.resolveNode":
		if !s.sent || int(params.(proto.DOMResolveNode).NodeID) != s.documents {
			return nil, &cdp.Error{Code: -32000, Message: "No node with given id found"}
		}
		return []byte(`{"object":{"type":"object","subtype":"node","objectId":"found"}}`), nil
	case "DOM.describeNode":
		return []byte(`{"node":{"nodeId":1,"backendNodeId":1,"nodeType":1,"nodeName":"A","localName":"a","nodeValue":""}}`), nil
	}
	return []byte(`{}`), nil
}

// count returns the number of answered commands of the session and method.
func (c *searchResetClient) count(session proto.TargetSessionID, method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[searchResetCall{string(session), method}]
}

// waitSearchResults returns when the endpoint has answered n
// DOM.getSearchResults requests. Before the first DOM.getDocument takes
// effect, each answer is a stale node ID.
func (c *searchResetClient) waitSearchResults(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		answers := 0
		for call, count := range c.calls {
			if call.method == "DOM.getSearchResults" {
				answers += count
			}
		}
		if answers >= n {
			return
		}
		c.answered.Wait()
	}
}

// newSearchResetPages returns a searchResetClient and an attached page for
// each target. The session of a page has the ID of its target.
func newSearchResetPages(t *testing.T, ctx context.Context, targets ...proto.TargetTargetID) (*searchResetClient, []*rod.Page) {
	t.Helper()
	client := &searchResetClient{
		events:   make(chan *cdp.Event),
		sessions: map[string]*searchResetSession{},
		calls:    map[searchResetCall]int{},
	}
	client.answered = sync.NewCond(&client.mu)
	browser := rod.New().Context(ctx).Monitor("").Trace(false).SlowMotion(0).NoDefaultDevice().Client(client)
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}
	pages := make([]*rod.Page, len(targets))
	for i, target := range targets {
		page, err := browser.PageFromTarget(target)
		if err != nil {
			t.Fatal(err)
		}
		pages[i] = page
	}
	return client, pages
}

// searchResetUse searches as a branch of a race does, with a sleeper that
// fails at its first sleep, then reads all results and releases them.
func searchResetUse(page *rod.Page) error {
	result, err := page.Sleeper(rod.NotFoundSleeper).Search("a")
	if err != nil {
		return fmt.Errorf("Search: %w", err)
	}
	_, err = result.All()
	if err != nil {
		err = fmt.Errorf("SearchResult.All: %w", err)
	}
	return errors.Join(err, result.Release())
}

// Two searches of one session find stale node IDs at the same time. The
// second DOM.getDocument request would reach the endpoint a millisecond after
// the first, when the search that sent the first holds new node IDs and has
// not resolved them, and would invalidate them. The searches share one reset
// instead, and both return their element without sleeping.
func TestSearchSharesNodeIDReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		client, pages := newSearchResetPages(t, ctx, "page")
		const searches = 2
		var resets atomic.Int32
		client.before = func(_ context.Context, _, method string) error {
			switch method {
			case "DOM.getDocument":
				// Both searches hold a stale node ID before the first reset
				// takes effect. A second reset takes effect a millisecond later.
				client.waitSearchResults(searches)
				if resets.Add(1) > 1 {
					time.Sleep(time.Millisecond)
				}
			case "DOM.resolveNode":
				// A search resolves its node two milliseconds after it
				// received the node ID.
				time.Sleep(2 * time.Millisecond)
			}
			return nil
		}

		errs := make(chan error, searches)
		for range searches {
			// Each search uses its own view of the page.
			go func() { errs <- searchResetUse(pages[0].Context(ctx)) }()
		}
		for range searches {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
		if got := client.count(pages[0].SessionID, "DOM.getDocument"); got != 1 {
			t.Errorf("DOM.getDocument requests = %d, want 1", got)
		}
	})
}

// A search that waits for the reset of another search returns when its own
// context ends, and the other search is not disturbed.
func TestSearchNodeIDResetWaitHonorsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		client, pages := newSearchResetPages(t, ctx, "page")
		// The endpoint answers the first reset when release closes.
		release := make(chan struct{})
		var resets atomic.Int32
		client.before = func(ctx context.Context, _, method string) error {
			if method == "DOM.getDocument" && resets.Add(1) == 1 {
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}

		first := make(chan error, 1)
		go func() { first <- searchResetUse(pages[0]) }()
		// The first search has sent its reset and waits for the answer.
		synctest.Wait()

		start := time.Now()
		waiting := pages[0].Timeout(time.Second)
		defer waiting.CancelTimeout()
		_, err := waiting.Search("a")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want %v", err, context.DeadlineExceeded)
		}
		if waited := time.Since(start); waited != time.Second {
			t.Fatalf("waited %s, want %s", waited, time.Second)
		}

		close(release)
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if got := client.count(pages[0].SessionID, "DOM.getDocument"); got != 1 {
			t.Fatalf("DOM.getDocument requests = %d, want 1", got)
		}
	})
}

// A search whose context ends while the endpoint holds its reset frees the
// reset for the search that waits for it, which then resets the node IDs
// itself and returns its element.
func TestSearchNodeIDResetAfterCanceledSearch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		client, pages := newSearchResetPages(t, ctx, "page")
		// The endpoint holds the first reset until the context of its search
		// ends, and does not apply it.
		var resets atomic.Int32
		client.before = func(ctx context.Context, _, method string) error {
			if method == "DOM.getDocument" && resets.Add(1) == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}

		canceled, cancelSearch := context.WithCancel(ctx)
		defer cancelSearch()
		first := make(chan error, 1)
		go func() { first <- searchResetUse(pages[0].Context(canceled)) }()
		// The first search has sent its reset and waits for the answer.
		synctest.Wait()
		second := make(chan error, 1)
		go func() { second <- searchResetUse(pages[0].Context(ctx)) }()
		// The second search holds a stale node ID and waits for the first.
		synctest.Wait()

		cancelSearch()
		if err := <-first; err == nil {
			t.Error("the canceled search returned no error")
		}
		if err := <-second; err != nil {
			t.Error(err)
		}
		if got := client.count(pages[0].SessionID, "DOM.getDocument"); got != 1 {
			t.Errorf("applied DOM.getDocument requests = %d, want 1", got)
		}
	})
}

// Each session numbers its own nodes. Searches of two sessions that find
// stale node IDs at the same time each reset the node IDs of their session.
func TestSearchNodeIDResetPerSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		client, pages := newSearchResetPages(t, ctx, "first", "second")
		// Both searches hold a stale node ID before a reset takes effect.
		client.before = func(_ context.Context, _, method string) error {
			if method == "DOM.getDocument" {
				client.waitSearchResults(len(pages))
			}
			return nil
		}

		errs := make(chan error, len(pages))
		for _, page := range pages {
			go func() { errs <- searchResetUse(page) }()
		}
		for range pages {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
		for _, page := range pages {
			if got := client.count(page.SessionID, "DOM.getDocument"); got != 1 {
				t.Errorf("DOM.getDocument requests of session %s = %d, want 1", page.SessionID, got)
			}
		}
	})
}
