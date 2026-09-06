// Run with: go run ./examples/launch-managed
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/fixture"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/launcher/flags"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	// A private ephemeral server makes the example self-contained. Authentication
	// remains enabled; use HTTPS/WSS for a manager on a non-loopback host.
	token := rand.Text()
	manager := launcher.NewManager(token)
	launched := make(chan *launcher.Launcher, 1)
	manager.BeforeLaunch = func(l *launcher.Launcher, _ http.ResponseWriter, _ *http.Request) {
		launched <- l
	}
	handlerFinished := make(chan struct{})
	server := &http.Server{
		BaseContext: func(net.Listener) context.Context { return ctx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				// Manager cleanup completes when this upgraded request returns.
				defer close(handlerFinished)
			}
			manager.ServeHTTP(w, r)
		}),
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	stopServer := context.AfterFunc(ctx, func() { _ = server.Close() })

	var client *cdp.Client
	var owned *launcher.Launcher
	cleaned := false
	cleanup := func() error {
		if cleaned {
			return nil
		}
		cleaned = true
		stopServer()
		cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		var cleanupErr error
		if client != nil {
			// Disconnecting the client asks the manager to stop its browser.
			if err := client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
		if err := server.Shutdown(cleanupCtx); err != nil {
			cleanupErr = errors.Join(cleanupErr, err, server.Close())
		}
		if owned == nil {
			select {
			case owned = <-launched:
			default:
			}
		}
		if owned != nil {
			select {
			case <-handlerFinished:
				cleanupErr = errors.Join(cleanupErr, owned.CleanupContext(cleanupCtx))
			case <-cleanupCtx.Done():
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("manager browser cleanup: %w", cleanupCtx.Err()))
			}
		}
		select {
		case err := <-served:
			if !errors.Is(err, http.ErrServerClosed) {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		case <-cleanupCtx.Done():
			cleanupErr = errors.Join(cleanupErr, cleanupCtx.Err())
		}
		return cleanupErr
	}
	defer func() { err = errors.Join(err, cleanup()) }()

	remote, err := launcher.NewManaged(ctx, "http://"+listener.Addr().String(), token)
	if err != nil {
		return err
	}
	client, err = remote.Context(ctx).Headless(true).Client()
	if err != nil {
		return err
	}
	select {
	case owned = <-launched:
	case <-ctx.Done():
		return ctx.Err()
	}
	profile := owned.Get(flags.UserDataDir)
	browser := rod.New().NoDefaultDevice().ControlURL("").Context(ctx).Client(client)
	if err := browser.Connect(); err != nil {
		return err
	}
	page, err := fixture.HTML(browser, `<!doctype html><h1>Managed browser</h1>`, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, page.Close()) }()
	heading, err := page.Page.Element("h1")
	if err != nil {
		return err
	}
	text, err := heading.Text()
	if err != nil {
		return err
	}
	if text != "Managed browser" {
		return fmt.Errorf("unexpected page heading: %q", text)
	}
	if err := page.Close(); err != nil {
		return err
	}
	if err := cleanup(); err != nil {
		return err
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("manager profile remains: %v", err)
	}
	_, err = fmt.Fprintf(output, "Authenticated manager page: %s\nManager process and profile: cleaned\n", text)
	return err
}
