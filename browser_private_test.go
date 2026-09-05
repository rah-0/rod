package rod

import (
	"errors"
	"os"
	"testing"

	"github.com/rah-0/rod/lib/launcher/flags"
)

func TestImplicitBrowserProcessCleanup(t *testing.T) {
	browser := New()
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}
	if browser.process == nil || browser.process.launcher.PID() == 0 {
		t.Fatal("implicit browser launch is not owned")
	}

	profile := browser.process.launcher.Get(flags.UserDataDir)
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("implicit browser profile was not created: %v", err)
	}
	if err := browser.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("implicit browser profile was not removed: %v", err)
	}
}
