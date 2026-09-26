// Run with: go run ./examples/proxy-auth
// Authenticate to a local HTTP proxy and verify that it forwarded the request.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"
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
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var proxyMarker, credentialsLeaked atomic.Bool
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	targetURL := &url.URL{Scheme: "http", Host: listener.Addr().String()}
	origin := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			proxyMarker.Store(r.Header.Get("X-Example-Proxy") == "authenticated")
			credentialsLeaked.Store(r.Header.Get("Proxy-Authorization") != "")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><h1 id="status">Authenticated proxy</h1>`)
		}),
	}
	originDone := make(chan error, 1)
	go func() { originDone <- origin.Serve(listener) }()
	defer func() {
		closeErr := origin.Close()
		serveErr := <-originDone
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		err = errors.Join(err, closeErr, serveErr)
	}()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 5 * time.Second
	defer transport.CloseIdleConnections()
	forward := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(targetURL)
			// Proxy credentials belong to the proxy, never to the origin server.
			r.Out.Header.Del("Proxy-Authorization")
			r.Out.Header.Set("X-Example-Proxy", "authenticated")
		},
	}
	const username, password = "example-user", "example-password"
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	var challenges, authenticated atomic.Int64
	proxyListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	proxyURL := "http://" + proxyListener.Addr().String()
	proxy := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Limit forwarding to this example's local HTTP target.
			if r.Method == http.MethodConnect || r.URL.Scheme != "http" || r.URL.Host != targetURL.Host {
				http.Error(w, "target is outside the example", http.StatusForbidden)
				return
			}
			if r.Header.Get("Proxy-Authorization") != expectedAuth {
				challenges.Add(1)
				w.Header().Set("Proxy-Authenticate", `Basic realm="example"`)
				w.WriteHeader(http.StatusProxyAuthRequired)
				return
			}
			authenticated.Add(1)
			forward.ServeHTTP(w, r)
		}),
	}
	proxyDone := make(chan error, 1)
	go func() { proxyDone <- proxy.Serve(proxyListener) }()
	defer func() {
		closeErr := proxy.Close()
		serveErr := <-proxyDone
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		err = errors.Join(err, closeErr, serveErr)
	}()

	browser := rod.New().ControlURL("").Context(ctx).NoDefaultDevice()
	options := launcher.New().Headless(true).Proxy(proxyURL).
		Set("proxy-bypass-list", "<-loopback>") // Chrome otherwise bypasses loopback proxies.
	if err := browser.Launch(options); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, browser.CloseWithTimeout(5*time.Second)) }()
	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return err
	}

	// Install authentication before navigation. Join its waiter on every exit.
	// Only challenges from this proxy receive the credentials.
	authCtx, stopAuth := context.WithCancel(ctx)
	handleAuth := browser.Context(authCtx).HandleAuth(rod.AuthCredentials{
		Source:   proto.FetchAuthChallengeSourceProxy,
		Origin:   proxyURL,
		Username: username,
		Password: password,
	})
	authDone := make(chan error, 1)
	go func() {
		defer close(authDone)
		authDone <- handleAuth()
	}()
	defer func() {
		stopAuth()
		err = errors.Join(err, <-authDone)
	}()
	if err := page.Navigate(targetURL.String()); err != nil {
		return err
	}
	if err := <-authDone; err != nil {
		return err
	}
	if err := page.WaitLoad(); err != nil {
		return err
	}
	var text string
	if err := page.EvalJSON(&text, `() => document.querySelector("#status").textContent`); err != nil {
		return err
	}
	if challenges.Load() == 0 || authenticated.Load() == 0 || !proxyMarker.Load() || credentialsLeaked.Load() {
		return fmt.Errorf("proxy verification failed: challenges=%d authenticated=%d marker=%t leaked=%t",
			challenges.Load(), authenticated.Load(), proxyMarker.Load(), credentialsLeaked.Load())
	}
	_, err = fmt.Fprintf(output, "Authenticated proxy: challenge=true, forwarded=true\nOrigin received proxy marker: %t\nProxy credentials forwarded to origin: %t\nPage: %s\n",
		proxyMarker.Load(), credentialsLeaked.Load(), text)
	return err
}
