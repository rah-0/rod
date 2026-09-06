// Run with: go run ./examples/owned-launch
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/launcher/flags"
	"github.com/rah-0/rod/lib/proto"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	bin, found := launcher.LookPath()
	if !found {
		return launcher.ErrBrowserNotFound
	}
	// A new launcher uses a fresh owned temporary profile by default. Supplying
	// UserDataDir instead would leave that directory owned by the caller.
	l := launcher.New().Bin(bin).Headless(true).RemoteDebuggingPort(0).
		Set("lang", "en-US").OutputTail(8 * 1024)
	profile := l.Get(flags.UserDataDir)
	browser := rod.New().ControlURL("").Context(ctx).NoDefaultDevice()
	// Launch errors include the recent stdout/stderr tail, bounded above to
	// 8 KiB. l.Output() also provides that tail after a successful launch.
	if err := browser.Launch(l); err != nil {
		return err
	}
	defer func() {
		// Repeated cleanup is safe, including after the explicit close below.
		err = errors.Join(err, browser.CloseWithTimeout(5*time.Second))
	}()

	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return err
	}
	if _, err := page.Evaluate(rod.Eval(`() => document.title = "Owned browser"`)); err != nil {
		return err
	}
	title, err := page.Evaluate(rod.Eval(`() => document.title`))
	if err != nil {
		return err
	}
	if title.Value.String() != "Owned browser" {
		return fmt.Errorf("unexpected page title: %s", title.Value.String())
	}

	// An operation deadline belongs to this context clone, not the browser's
	// process lifetime. Use an already expired deadline without sleeping.
	expired, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	_, operationErr := page.Context(expired).Evaluate(rod.Eval(`() => document.title`))
	if !errors.Is(operationErr, context.DeadlineExceeded) {
		return fmt.Errorf("expected operation deadline, got %v", operationErr)
	}
	// Shutdown gets its own finite budget even on the expired context clone.
	if err := browser.Context(expired).CloseWithTimeout(5 * time.Second); err != nil {
		return err
	}
	select {
	case <-l.Done():
	default:
		return errors.New("owned browser process cleanup did not complete")
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("owned temporary profile remains: %v", err)
	}
	_, err = fmt.Fprintf(output, "title: %s\nexpired operation: deadline exceeded\nowned process: stopped\ntemporary profile: removed\n", title.Value.String())
	return err
}
