package rod

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestDefaultSleeperBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sleep := DefaultSleeper()
		previous := 10 * time.Millisecond
		for range 10 {
			start := time.Now()
			if err := sleep(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := time.Since(start)
			low := min(previous*19/10, time.Second)
			high := min(previous*21/10, time.Second)
			if got < low || got > high {
				t.Fatalf("sleep = %s, want between %s and %s", got, low, high)
			}
			previous = got
		}
	})
}
