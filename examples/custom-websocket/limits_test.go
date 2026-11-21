package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/gobwas/ws"
	"github.com/rah-0/rod/lib/cdp"
)

func TestMessageLimit(t *testing.T) {
	for _, tail := range []string{"cd", "cde"} {
		t.Run(tail, func(t *testing.T) {
			w, peer := pipeTransport(t)
			w.MaxMessageSize = 4
			frames := append(ws.MustCompileFrame(ws.NewFrame(ws.OpText, false, []byte("ab"))), ws.MustCompileFrame(ws.NewFrame(ws.OpPing, true, []byte("ping")))...)
			frames = append(frames, ws.MustCompileFrame(ws.NewFrame(ws.OpContinuation, true, []byte(tail)))...)
			w.Reader = bytes.NewReader(frames)
			replies := make(chan error, 1)
			go func() {
				pong, err := ws.ReadFrame(peer)
				if err == nil && pong.Header.OpCode != ws.OpPong {
					err = errors.New("missing pong")
				}
				if err == nil && tail == "cde" {
					closeFrame, readErr := ws.ReadFrame(peer)
					err = readErr
					if err == nil {
						ws.Cipher(closeFrame.Payload, closeFrame.Header.Mask, 0)
						code, _ := ws.ParseCloseFrameData(closeFrame.Payload)
						if closeFrame.Header.OpCode != ws.OpClose || code != ws.StatusMessageTooBig {
							err = errors.New("missing close 1009")
						}
					}
				}
				replies <- err
			}()
			data, err := w.Read()
			if tail == "cd" {
				if err != nil || string(data) != "abcd" {
					t.Fatalf("exact limit: %q, %v", data, err)
				}
			} else if !errors.Is(err, cdp.ErrWebSocketMessageTooLarge) || data != nil {
				t.Fatalf("over limit: %q, %v", data, err)
			}
			if err := <-replies; err != nil {
				t.Fatal(err)
			}
		})
	}
}
