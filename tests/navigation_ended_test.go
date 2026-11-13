package rod_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

// navigationErrors lists the answers of Chrome to an evaluation that the
// document did not finish before a navigation replaced it.
var navigationErrors = map[string]error{
	"context destroyed": cdp.ErrCtxDestroyed,
	"target navigated":  &cdp.Error{Code: -32000, Message: "Inspected target navigated or closed"},
}

// navigationNode is the result of an evaluation that returns an element.
const navigationNode = `{"result":{"type":"object","subtype":"node","objectId":"found"}}`

// isEvaluation reports whether call is the function in which Page.Evaluate
// wraps its JavaScript, and not the installation of a JavaScript helper.
func isEvaluation(call proto.RuntimeCallFunctionOn) bool {
	return strings.HasPrefix(call.FunctionDeclaration, "function() { return (")
}

// navigationHelper answers the installation of a JavaScript helper.
const navigationHelper = `{"result":{"type":"object","objectId":"helper"}}`

// navigationFrame returns the page of a frame that is part of the session of
// its parent.
func navigationFrame(env *lenientEnv) (*rod.Page, error) {
	env.respond("DOM.describeNode", `{"node":{"nodeId":1,"backendNodeId":1,"nodeType":1,"nodeName":"IFRAME","localName":"iframe","nodeValue":"","frameId":"frame",`+
		`"contentDocument":{"nodeId":2,"backendNodeId":2,"nodeType":9,"nodeName":"#document","localName":"","nodeValue":""}}}`)
	env.respond("DOM.resolveNode", `{"object":{"type":"object","objectId":"document"}}`)
	return env.element.Frame()
}

// An evaluation that a navigation ends returns Chrome's error, which matches
// cdp.ErrCtxDestroyed, and is not repeated, so the caller learns of the
// navigation and decides whether to go on in the new document. This holds for
// the waits and queries of a page, for single evaluations, for calls on an
// element, and for Page.HTML of a frame in the session of its parent.
func TestNavigationEndsEvaluation(t *testing.T) {
	frame := func(run func(*rod.Page) error) func(*lenientEnv) error {
		return func(env *lenientEnv) error {
			page, err := navigationFrame(env)
			if err != nil {
				return err
			}
			return run(page)
		}
	}
	for _, test := range []struct {
		name string
		run  func(*lenientEnv) error
	}{
		{"Page.Wait", func(env *lenientEnv) error { return env.page.Wait(rod.Eval(`() => true`)) }},
		{"Page.WaitElementsMoreThan", func(env *lenientEnv) error { return env.page.WaitElementsMoreThan("a", 0) }},
		{"Page.Element", func(env *lenientEnv) error { _, err := env.page.Element("a"); return err }},
		{"Page.ElementR", func(env *lenientEnv) error { _, err := env.page.ElementR("a", "text"); return err }},
		{"Page.ElementX", func(env *lenientEnv) error { _, err := env.page.ElementX("//a"); return err }},
		{"Page.ElementByJS", func(env *lenientEnv) error {
			_, err := env.page.ElementByJS(rod.Eval(`() => document.body`))
			return err
		}},
		{"Page.Has", func(env *lenientEnv) error { _, _, err := env.page.Has("a"); return err }},
		{"RaceContext.Do", func(env *lenientEnv) error { _, err := env.page.Race().Element("a").Do(); return err }},
		{"Page.Eval", func(env *lenientEnv) error { _, err := env.page.Eval(`() => 1`); return err }},
		{"Page.Evaluate", func(env *lenientEnv) error { _, err := env.page.Evaluate(rod.Eval(`() => 1`)); return err }},
		{"Page.Elements", func(env *lenientEnv) error { _, err := env.page.Elements("a"); return err }},
		{"Element.Wait", func(env *lenientEnv) error { return env.element.Wait(rod.Eval(`() => true`)) }},
		{"Element.WaitVisible", func(env *lenientEnv) error { return env.element.WaitVisible() }},
		{"Element.Element", func(env *lenientEnv) error { _, err := env.element.Element("a"); return err }},
		{"Element.ElementByJS", func(env *lenientEnv) error {
			_, err := env.element.ElementByJS(rod.Eval(`() => this.firstElementChild`))
			return err
		}},
		{"Page.Wait in a frame", frame(func(p *rod.Page) error { return p.Wait(rod.Eval(`() => true`)) })},
		{"Page.Element in a frame", frame(func(p *rod.Page) error { _, err := p.Element("a"); return err })},
		{"Page.HTML in a frame", frame(func(p *rod.Page) error { _, err := p.HTML(); return err })},
	} {
		for name, ended := range navigationErrors {
			t.Run(test.name+"/"+name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
					defer cancel()
					// The navigation ends the first evaluation. A later
					// evaluation fails in another way, so a call that
					// evaluates again returns that error and fails the test
					// instead of evaluating without end.
					var evaluations atomic.Int32
					env := newLenientEnv(t, ctx, proto.DecodeStrict, lenientCase{
						answer: func(_ string, params any) (string, error) {
							call, ok := params.(proto.RuntimeCallFunctionOn)
							if !ok {
								return "", nil
							}
							if !isEvaluation(call) {
								return navigationHelper, nil
							}
							if evaluations.Add(1) == 1 {
								return "", ended
							}
							return "", errors.New("evaluated again after the navigation")
						},
					})

					if err := test.run(env); !errors.Is(err, cdp.ErrCtxDestroyed) {
						t.Fatalf("error = %v, want one matching cdp.ErrCtxDestroyed", err)
					}
					if evaluations.Load() != 1 {
						t.Fatalf("evaluations = %d, want 1", evaluations.Load())
					}
				})
			})
		}
	}
}

// Page.HTML of a frame in the session of its parent returns the error of a
// read whose handle went with a replaced document, and does not query again:
// the replaced document can be the parent's.
func TestPageHTMLInFrameAfterReplacedDocument(t *testing.T) {
	for name, stale := range map[string]error{
		"context not found": cdp.ErrCtxNotFound,
		"context destroyed": cdp.ErrCtxDestroyed,
		"object not found":  cdp.ErrObjNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
				defer cancel()
				var evaluations, reads atomic.Int32
				env := newLenientEnv(t, ctx, proto.DecodeStrict, lenientCase{
					answer: func(_ string, params any) (string, error) {
						switch call := params.(type) {
						case proto.RuntimeCallFunctionOn:
							if !isEvaluation(call) {
								return navigationHelper, nil
							}
							evaluations.Add(1)
							return navigationNode, nil
						case proto.DOMGetOuterHTML:
							// Only the first read finds its handle gone. A
							// later read returns markup, so a call that reads
							// again returns no error and fails the test
							// instead of reading without end.
							if reads.Add(1) > 1 {
								return `{"outerHTML":"<html></html>"}`, nil
							}
							return "", stale
						}
						return "", nil
					},
				})
				frame, err := navigationFrame(env)
				if err != nil {
					t.Fatal(err)
				}

				if _, err := frame.HTML(); !errors.Is(err, stale) {
					t.Fatalf("error = %v, want one matching %v", err, stale)
				}
				if evaluations.Load() != 1 {
					t.Fatalf("evaluations = %d, want 1", evaluations.Load())
				}
			})
		})
	}
}
