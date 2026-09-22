package main_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/proto"
)

func BenchmarkPageEachEvent(b *testing.B) {
	for _, pages := range []int{1, 8} {
		for _, burst := range []int{1, 32} {
			b.Run(fmt.Sprintf("pages=%d/events=%d", pages, burst), func(b *testing.B) {
				page := benchmarkWaitPage(b)
				for i := 1; i < pages; i++ {
					page.Browser().MustPage("about:blank")
				}
				ctx, cancel := context.WithCancel(page.GetContext())
				ack := make(chan struct{}, 1)
				done := make(chan error, 1)
				count := 0
				wait := page.Context(ctx).EachEvent(rod.On(func(event *proto.RuntimeConsoleAPICalled, _ proto.TargetSessionID) bool {
					if len(event.Args) == 0 || event.Args[0].Value.Str() != "rod-benchmark" {
						return false
					}
					count++
					if count == burst {
						count = 0
						ack <- struct{}{}
					}
					return false
				}))
				go func() { done <- wait() }()
				b.Cleanup(func() {
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) {
						b.Errorf("event subscription cleanup: %v", err)
					}
				})
				b.ReportAllocs()
				for b.Loop() {
					if _, err := page.Eval(`count => {
						for (let i = 0; i < count; i++) console.log('rod-benchmark');
					}`, burst); err != nil {
						b.Fatal(err)
					}
					select {
					case <-ack:
					case err := <-done:
						done <- err
						b.Fatalf("event subscription ended before the burst arrived: %v", err)
					case <-b.Context().Done():
						b.Fatal(b.Context().Err())
					}
				}
				b.ReportMetric(float64(burst), "events/op")
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*burst), "ns/event")
			})
		}
	}
}
