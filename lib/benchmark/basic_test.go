// Example run:
// go test -bench . ./lib/benchmark

package main_test

import (
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/launcher"
)

func BenchmarkCleanup(b *testing.B) {
	u := testutil.New(b).Serve().Route("/", "", "page body").URL("/")

	// Each operation includes browser launch, page work, shutdown, and profile removal.
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			func() {
				launch := launcher.New()
				defer func() {
					launch.Kill()
					launch.Cleanup()
				}()
				url := launch.MustLaunch()
				browser := rod.New().Context(b.Context()).ControlURL(url).MustConnect()
				defer browser.MustClose()
				browser.MustPage(u).MustClose()
			}()
		}
	})
}
