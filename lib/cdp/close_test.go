package cdp

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"testing/synctest"
)

type closeSocket struct {
	frames chan []byte
	closed chan struct{}
	once   sync.Once
	closes int
}

func (s *closeSocket) Send([]byte) error { return nil }
func (s *closeSocket) Read() ([]byte, error) {
	select {
	case data := <-s.frames:
		return data, nil
	case <-s.closed:
		return nil, io.EOF
	}
}
func (s *closeSocket) Close() error { s.closes++; s.once.Do(func() { close(s.closed) }); return nil }

func TestClientCloseUnreadEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &closeSocket{frames: make(chan []byte, 1), closed: make(chan struct{})}
		transport.frames <- []byte(`{"method":"Target.targetCreated","params":{}}`)
		client := New().Start(transport)
		synctest.Wait() // The reader is waiting to deliver its event.
		result := make(chan error, 1)
		go func() { _, err := client.Call(context.Background(), "", "Browser.getVersion", nil); result <- err }()
		synctest.Wait()
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case err := <-result:
			if !errors.Is(err, ErrClientClosed) {
				t.Fatalf("pending call: %v", err)
			}
		default:
			t.Fatal("Close left a pending request blocked")
		}
		if _, open := <-client.Event(); open {
			t.Fatal("reader retained its event after Close")
		}
		if _, err := client.Call(t.Context(), "", "later", nil); !errors.Is(err, ErrClientClosed) {
			t.Fatal(err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		if transport.closes != 1 {
			t.Fatalf("transport closed %d times", transport.closes)
		}
	})
}
