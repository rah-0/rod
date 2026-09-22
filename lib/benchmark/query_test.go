package main_test

import (
	"fmt"
	"testing"
)

func BenchmarkPageElements(b *testing.B) {
	for _, count := range []int{1, 100} {
		b.Run(fmt.Sprintf("elements=%d", count), func(b *testing.B) {
			page := benchmarkWaitPage(b)
			page.MustEval(`count => {
				for (let i = 0; i < count; i++) document.body.append(document.createElement('span'));
			}`, count)
			page.MustElement("span").MustRelease()
			b.ReportAllocs()
			for b.Loop() {
				elements, err := page.Elements("span")
				if err != nil {
					b.Fatal(err)
				}
				if len(elements) != count {
					b.Fatalf("query returned %d elements, want %d", len(elements), count)
				}
				b.StopTimer()
				for _, element := range elements {
					if err := element.Release(); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
			}
			b.ReportMetric(float64(count), "elements/op")
		})
	}
}
