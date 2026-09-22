package cdp_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/utils"
)

var setup = testutil.Setup(nil)

func TestBasic(t *testing.T) {
	g := setup(t)

	ctx := g.Context()

	l := launcher.New()
	g.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})
	u := l.MustLaunch()

	client := cdp.New().Logger(defaults.CDP).Start(cdp.MustConnectWS(u))

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = client.Call(ctx, "", "Browser.close", nil)
	}()

	go func() {
		for range client.Event() {
			utils.Noop()
		}
	}()

	file, err := filepath.Abs(filepath.FromSlash("../../../lib/cdp/fixtures/iframe.html"))
	g.E(err)

	res, err := client.Call(ctx, "", "Target.createTarget", map[string]string{
		"url": "file://" + file,
	})
	g.E(err)

	targetID := jsonvalue.New(res).Get("targetId").String()

	res, err = client.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": targetID,
		"flatten":  true, // if it's not set no response will return
	})
	g.E(err)

	sessionID := jsonvalue.New(res).Get("sessionId").String()

	_, err = client.Call(ctx, sessionID, "Page.enable", nil)
	g.E(err)

	_, err = client.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": "abc",
	})
	g.Err(err)

	timeout := g.Context()

	sleeper := func() utils.Sleeper {
		return utils.BackoffSleeper(30*time.Millisecond, 3*time.Second, nil)
	}

	// cancel call
	tmpCtx, tmpCancel := context.WithCancel(ctx)
	tmpCancel()
	_, err = client.Call(tmpCtx, sessionID, "Runtime.evaluate", map[string]any{
		"expression": `10`,
	})
	g.Eq(err.Error(), context.Canceled.Error())

	g.E(utils.Retry(timeout, sleeper(), func() (bool, error) {
		res, err = client.Call(ctx, sessionID, "Runtime.evaluate", map[string]any{
			"expression": `document.querySelector('iframe')`,
		})

		return err == nil && jsonvalue.New(res).Get("result.subtype").String() != "null", nil
	}))

	res, err = client.Call(ctx, sessionID, "DOM.describeNode", map[string]any{
		"objectId": jsonvalue.New(res).Get("result.objectId").String(),
	})
	g.E(err)

	frameID := jsonvalue.New(res).Get("node.frameId").String()

	timeout = g.Context()

	g.E(utils.Retry(timeout, sleeper(), func() (bool, error) {
		// we might need to recreate the world because world can be
		// destroyed after the frame is reloaded
		res, err = client.Call(ctx, sessionID, "Page.createIsolatedWorld", map[string]any{
			"frameId": frameID,
		})
		g.E(err)

		res, err = client.Call(ctx, sessionID, "Runtime.evaluate", map[string]any{
			"contextId":  jsonvalue.New(res).Get("executionContextId").Int(),
			"expression": `document.querySelector('h4')`,
		})

		return err == nil && jsonvalue.New(res).Get("result.subtype").String() != "null", nil
	}))

	res, err = client.Call(ctx, sessionID, "DOM.getOuterHTML", map[string]any{
		"objectId": jsonvalue.New(res).Get("result.objectId").String(),
	})
	g.E(err)

	g.Eq("<h4>it works</h4>", jsonvalue.New(res).Get("outerHTML").String())
}

func TestCrash(t *testing.T) {
	g := setup(t)

	ctx := g.Context()

	l := launcher.New()
	g.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})
	u := l.MustLaunch()

	client := cdp.MustStartWithURL(ctx, u, nil)

	go func() {
		for range client.Event() {
			utils.Noop()
		}
	}()

	file, err := filepath.Abs(filepath.FromSlash("../../../lib/cdp/fixtures/iframe.html"))
	g.E(err)

	res, err := client.Call(ctx, "", "Target.createTarget", map[string]any{
		"url": "file://" + file,
	})
	g.E(err)

	targetID := jsonvalue.New(res).Get("targetId").String()

	res, err = client.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": targetID,
		"flatten":  true,
	})
	g.E(err)

	sessionID := jsonvalue.New(res).Get("sessionId").String()

	_, err = client.Call(ctx, sessionID, "Page.enable", nil)
	g.E(err)

	go func() {
		utils.Sleep(1)
		_, err := client.Call(ctx, sessionID, "Browser.crash", nil)
		g.True(errors.Is(err, io.EOF))
	}()

	_, err = client.Call(ctx, sessionID, "Runtime.evaluate", map[string]any{
		"expression":   `new Promise(() => {})`,
		"awaitPromise": true,
	})
	g.True(errors.Is(err, io.EOF))

	_, err = client.Call(ctx, sessionID, "Runtime.evaluate", map[string]any{
		"expression": `10`,
	})
	g.True(errors.Is(err, io.EOF))
}

func TestMassBrowserClose(t *testing.T) {
	t.Skip()

	g := setup(t)
	s := g.Serve()

	for i := range 50 {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			t.Parallel()
			browser := rod.New().MustConnect()
			browser.MustPage(s.URL()).MustWaitLoad().MustClose()
			browser.MustClose()
		})
	}
}
