package rod_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rah-0/rod"
)

func TestMonitorCancellation(t *testing.T) {
	for _, timing := range []string{"before serve", "after serve"} {
		t.Run(timing, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if timing == "before serve" {
				cancel()
			}
			u := rod.New().Context(ctx).ServeMonitor("")
			transport := &http.Transport{DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			if timing == "after serve" {
				res, err := client.Get(u)
				if err != nil {
					t.Fatal(err)
				}
				_ = res.Body.Close()
				if res.StatusCode != http.StatusOK {
					t.Fatalf("monitor returned %s", res.Status)
				}
				cancel()
			}

			deadline := time.Now().Add(5 * time.Second)
			for {
				conn, err := net.DialTimeout("tcp", strings.TrimPrefix(u, "http://"), time.Second)
				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						t.Fatalf("monitor connection timed out: %v", err)
					}
					return
				}
				_ = conn.Close()
				if time.Now().After(deadline) {
					t.Fatal("canceled monitor is still accepting requests")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
