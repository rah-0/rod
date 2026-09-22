// Run with: go run ./examples/tab-metadata
// Read tab metadata without activating tabs. See README.md for availability and polling.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
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

	browser := rod.New().ControlURL("").Context(ctx).NoDefaultDevice()
	if err := browser.Launch(launcher.New().Headless(true)); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.CloseWithTimeout(5*time.Second)) }()

	// Give this private browser a tab to inspect. An existing browser only needs
	// the query below; it does not need target creation or activation.
	if _, err := (proto.TargetCreateTarget{URL: "about:blank", Background: new(true)}).Call(browser); err != nil {
		return err
	}
	return writeTabs(browser, output)
}

func writeTabs(client proto.Client, output io.Writer) error {
	result, err := (proto.TargetGetTargets{Filter: proto.TargetTargetFilter{
		{Type: "tab"},
		{Exclude: true},
	}}).Call(client)
	if err != nil {
		return err
	}
	// Keep embedderData as supplied, including unknown keys and absent fields.
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
