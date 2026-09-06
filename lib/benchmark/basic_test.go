// Example run:
// go test -bench . ./lib/benchmark

package main_test

import (
	"context"
	"testing"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/launcher"
)

func BenchmarkCleanup(b *testing.B) {
	u := testutil.New(b).Serve().Route("/", "", "page body").URL("/")

	// Each operation includes browser launch, page work, shutdown, and profile removal.
	for b.Loop() {
		func() {
			ctx, cancel := context.WithTimeout(b.Context(), time.Minute)
			defer cancel()
			launch := launcher.New().Context(ctx)
			defer func() {
				launch.Kill()
				launch.Cleanup()
			}()
			url := launch.MustLaunch()
			browser := rod.New().Context(ctx).ControlURL(url).MustConnect()
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				stop := context.AfterFunc(ctx, launch.Kill)
				defer stop()
				_ = browser.Context(ctx).Close()
			}()
			browser.MustPage(u).MustClose()
		}()
	}
}
