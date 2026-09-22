package utils_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rah-0/rod/lib/utils"
)

func TestBackoffSleeperWakeNow(t *testing.T) {
	g := setup(t)

	g.E(utils.BackoffSleeper(0, 0, nil)(g.Context()))
}

func TestBackoffSleeperCapsGrowth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sleep := utils.BackoffSleeper(2*time.Millisecond, 5*time.Millisecond, func(d time.Duration) time.Duration { return 2 * d })
		for _, want := range []time.Duration{4 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond} {
			start := time.Now()
			if err := sleep(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := time.Since(start); got != want {
				t.Fatalf("sleep = %s, want %s", got, want)
			}
		}
	})
}

func TestRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := setup(t)

		count := 0
		s1 := utils.BackoffSleeper(time.Millisecond, 5*time.Millisecond, func(d time.Duration) time.Duration { return 2 * d })

		err := utils.Retry(g.Context(), s1, func() (bool, error) {
			if count > 5 {
				return true, io.EOF
			}
			count++
			return false, nil
		})

		g.True(errors.Is(err, io.EOF))
	})
}

func TestRetryCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := setup(t)

		ctx := g.Context()
		go ctx.Cancel()
		s := utils.BackoffSleeper(time.Second, time.Second, nil)

		err := utils.Retry(ctx, s, func() (bool, error) {
			return false, nil
		})

		g.True(errors.Is(err, context.Canceled))
	})
}

func TestCountSleeperErr(t *testing.T) {
	g := setup(t)

	ctx := g.Context()
	s := utils.CountSleeper(5)
	for range 5 {
		_ = s(ctx)
	}
	g.Err(s(ctx))
}

func TestCountSleeperCancel(t *testing.T) {
	g := setup(t)

	s := utils.CountSleeper(5)
	g.Eq(s(g.Timeout(0)), context.DeadlineExceeded)
}

func TestEachSleepers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := setup(t)

		s1 := utils.BackoffSleeper(time.Millisecond, 5*time.Millisecond, func(d time.Duration) time.Duration { return 2 * d })
		s2 := utils.CountSleeper(5)
		s := utils.EachSleepers(s1, s2)

		err := utils.Retry(t.Context(), s, func() (stop bool, err error) {
			return false, nil
		})

		g.Is(err, &utils.MaxSleepCountError{})
		g.Eq(err.Error(), "max sleep count 5 exceeded")
	})
}

func TestRaceSleepers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := setup(t)

		s1 := utils.BackoffSleeper(time.Millisecond, 5*time.Millisecond, func(d time.Duration) time.Duration { return 2 * d })
		s2 := utils.CountSleeper(5)
		s := utils.RaceSleepers(s1, s2)

		err := utils.Retry(t.Context(), s, func() (stop bool, err error) {
			return false, nil
		})

		g.Is(err, &utils.MaxSleepCountError{})
		g.Eq(err.Error(), "max sleep count 5 exceeded")
	})
}
