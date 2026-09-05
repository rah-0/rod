package cdp_test

import (
	"errors"
	"testing"

	"github.com/rah-0/rod/lib/cdp"
)

func TestContextDestroyedErrors(t *testing.T) {
	for _, message := range []string{
		"Execution context was destroyed.",
		"Inspected target navigated or closed",
	} {
		err := &cdp.Error{Code: -32000, Message: message}
		if !errors.Is(err, cdp.ErrCtxDestroyed) {
			t.Fatalf("context destruction message %q was not classified", message)
		}
	}

	if errors.Is(&cdp.Error{Code: -32000, Message: "unrelated"}, cdp.ErrCtxDestroyed) {
		t.Fatal("unrelated protocol error was classified as context destruction")
	}
}
