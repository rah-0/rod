package rod_test

import (
	"errors"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/cdp"
)

func TestBrowserResetControlURL(_ *testing.T) {
	rod.New().ControlURL("test").ControlURL("")
}

func TestTestTry(t *testing.T) {
	g := testutil.New(t)

	g.Nil(rod.Try(func() {}))

	err := rod.Try(func() { panic(1) })
	errVal, ok := errors.AsType[*rod.TryError](err)
	g.True(ok)
	g.Is(err, &rod.TryError{})
	g.Eq(errVal.Unwrap().Error(), "1")
	g.Eq(1, errVal.Value)
	g.Has(errVal.Error(), "error value: 1\ngoroutine")

	errVal = rod.Try(func() { panic(errors.New("t")) }).(*rod.TryError)
	g.Eq(errVal.Unwrap().Error(), "t")
}

func TestBrowserConnectFailure(t *testing.T) {
	g := testutil.New(t)

	c := g.Context()
	c.Cancel()
	err := rod.New().Context(c).Connect()
	if err == nil {
		g.Fatal("expected an error on connect failure")
	}
}

func TestBrowserConnectConflict(t *testing.T) {
	g := testutil.New(t)
	g.Panic(func() {
		rod.New().Client(&cdp.Client{}).ControlURL("test").MustConnect()
	})
}
