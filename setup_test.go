package rod_test

import (
	"log"
	"os"
	"testing"

	"github.com/rah-0/rod/internal/goroutines"
	"github.com/rah-0/rod/lib/defaults"
)

func TestMain(m *testing.M) {
	defaults.Load()
	if code := m.Run(); code != 0 {
		os.Exit(code)
	}
	if err := goroutines.Check(0, goroutines.Functions("internal/poll.runtime_pollWait")); err != nil {
		log.Fatal(err)
	}
}
