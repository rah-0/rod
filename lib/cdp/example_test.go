package cdp_test

import (
	"context"
	"fmt"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

func ExampleClient() {
	ctx := context.Background()

	// launch a browser
	l := launcher.New()
	url := l.MustLaunch()
	defer func() {
		l.Kill()
		l.Cleanup()
	}()

	// create a controller
	client := cdp.New().Start(cdp.MustConnectWS(url))

	go func() {
		for range client.Event() {
			// you must consume the events
			utils.Noop()
		}
	}()

	// Such as call this endpoint on the api doc:
	// https://chromedevtools.github.io/devtools-protocol/tot/Page#method-navigate
	// This will create a new tab and navigate to the test.com
	res, err := client.Call(ctx, "", "Target.createTarget", map[string]string{
		"url": "http://test.com",
	})
	utils.E(err)

	fmt.Println(len(jsonvalue.New(res).Get("targetId").Str()))

	// close browser by using the proto lib to encode json
	_ = proto.BrowserClose{}.Call(client)

	// Output: 32
}

func Example_customize_cdp_log() {
	l := launcher.New()
	u := l.MustLaunch()
	defer func() {
		l.Kill()
		l.Cleanup()
	}()

	ws := cdp.MustConnectWS(u)

	client := cdp.New().
		Logger(utils.Log(func(args ...any) {
			switch v := args[0].(type) {
			case *cdp.Request:
				fmt.Printf("id: %d", v.ID)
			}
		})).
		Start(ws)

	_ = proto.BrowserClose{}.Call(client)
}
