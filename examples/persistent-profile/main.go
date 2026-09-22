// Run with: go run ./examples/persistent-profile -profile /path/to/rod-automation-profile
// Use a dedicated non-default profile. See README.md for separate login setup.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

func main() {
	profile := flag.String("profile", "", "dedicated non-default automation profile directory")
	defaults.Load()
	flag.Parse()
	if err := run(context.Background(), os.Stdout, *profile); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer, profile string) (err error) {
	if profile == "" {
		return errors.New("set -profile to a dedicated non-default automation directory")
	}
	profile, err = filepath.Abs(profile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	// UserDataDir keeps this directory caller-owned instead of selecting a
	// temporary profile. Later runs use the same browser state.
	l := launcher.New().UserDataDir(profile).Headless(true).RemoteDebuggingPort(0)
	browser := rod.New().ControlURL("").Context(ctx).NoDefaultDevice()
	if err := browser.Launch(l); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.CloseWithTimeout(5*time.Second)) }()

	// Pages in the default browser context use the supplied profile. Put the
	// desired automation here after establishing its separate site logins.
	if _, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Persistent automation profile: %s\n", profile)
	return err
}
