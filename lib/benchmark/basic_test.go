// Example run:
// go test -bench . ./lib/benchmark

package main_test

import (
	"path/filepath"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/utils"
)

func BenchmarkCleanup(b *testing.B) {
	u := testutil.New(b).Serve().Route("/", "", "page body").URL("/")

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			launch := launcher.New().UserDataDir(filepath.Join("tmp", "cleanup", utils.RandString(8)))
			url := launch.MustLaunch()
			b.Cleanup(func() {
				launch.Kill()
				launch.Cleanup()
			})

			browser := rod.New().ControlURL(url).MustConnect()
			b.Cleanup(browser.MustClose)

			browser.MustPage(u).MustClose()
		}
	})
}
