package launcher_test

import (
	"context"
	"math/rand"
	"sync"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/utils"
)

func BenchmarkManager(b *testing.B) {
	const managerToken = "test-manager-token-0123456789abcdef0123456789abcdef"

	const concurrent = 30 // how many browsers will run at the same time
	const num = 300       // how many browsers we will launch

	limiter := make(chan int, concurrent)

	s := testutil.New(b).Serve()
	manager := testutil.New(b).Serve()
	manager.Mux.Handle("/", launcher.NewManager(managerToken))

	s.Route("/", ".html", `<html><body>
		ok
	</body><script>
		function wait() {
			return new Promise(r => setTimeout(r, 1000 * Math.random()))
		}
	</script></html>`)

	wg := &sync.WaitGroup{}
	wg.Add(num)
	for i := 0; i < num; i++ {
		limiter <- 0

		go func() {
			utils.Sleep(rand.Float64())

			ctx, cancel := context.WithCancel(context.Background())
			defer func() {
				go func() {
					utils.Sleep(2)
					cancel()
				}()
			}()

			l := launcher.MustNewManaged(manager.URL(), managerToken)
			u, h := l.ClientHeader()
			browser := rod.New().Client(cdp.MustStartWithURL(ctx, u, h)).MustConnect()
			page := browser.MustPage()
			wait := page.MustWaitNavigation()
			page.MustNavigate(s.URL())
			wait()
			page.MustEval(`wait()`)

			if rand.Int()%10 == 0 {
				// 10% we will drop the websocket connection without call the api to gracefully close the browser
				cancel()
			} else {
				browser.MustClose()
			}

			wg.Done()
			<-limiter
		}()
	}
	wg.Wait()
}
