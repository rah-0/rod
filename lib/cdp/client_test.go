package cdp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/rah-0/rod/internal/goroutines"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/jsonvalue"
	"github.com/rah-0/rod/lib/utils"
)

var setup = testutil.Setup(nil)

func TestError(t *testing.T) {
	g := setup(t)

	cdpErr := cdp.Error{10, "err", "data"}
	g.Eq(cdpErr.Error(), "{10 err data}")
	g.True(cdpErr.Is(&cdpErr))

	g.Panic(func() {
		cdp.MustStartWithURL(context.Background(), "", nil)
	})
}

func TestFormat(t *testing.T) {
	g := setup(t)

	g.Eq(cdp.Request{
		ID:        123,
		SessionID: "000000001234",
		Method:    "test",
		Params:    1,
	}.String(), `=> #123 @00000000 test 1`)

	g.Eq(cdp.Response{
		ID:     0,
		Result: []byte("11"),
	}.String(), "<= #0 11")

	g.Eq(cdp.Response{Error: &cdp.Error{}}.String(), `<= #0 error: {"code":0,"message":"","data":""}`)

	g.Eq(cdp.Event{
		Method: "event",
		Params: []byte("11"),
	}.String(), `<- @00000000 event 11`)
}

func TestSlowSend(t *testing.T) {
	g := setup(t)

	goroutines.CheckLeak(g, 0)

	id := 0
	wait := make(chan int)

	ws := &MockWebSocket{
		send: func([]byte) error {
			close(wait)
			utils.Sleep(0.3)
			return nil
		},
		read: func() ([]byte, error) {
			if id > 0 {
				return nil, io.EOF
			}

			id++
			<-wait

			return json.Marshal(cdp.Response{
				ID:     id,
				Result: json.RawMessage("1"),
				Error:  nil,
			})
		},
	}

	c := cdp.New().Start(ws)
	_, err := c.Call(g.Context(), "1234567890", "method", 1)
	g.E(err)
}

func TestCancelCallLeak(t *testing.T) {
	g := setup(t)

	goroutines.CheckLeak(g, 0)

	for range 30 {
		id := 0
		wait := make(chan int)

		ws := &MockWebSocket{
			send: func([]byte) error {
				close(wait)
				utils.Sleep(0.01)
				return nil
			},
			read: func() ([]byte, error) {
				if id > 0 {
					return nil, io.EOF
				}

				id++
				<-wait

				return json.Marshal(cdp.Response{
					ID:     id,
					Result: json.RawMessage("1"),
					Error:  nil,
				})
			},
		}

		c := cdp.New().Start(ws)
		ctx := g.Context()
		ctx.Cancel()
		_, _ = c.Call(ctx, "1234567890", "method", 1)
	}
}

func TestConcurrentCall(t *testing.T) {
	g := setup(t)

	goroutines.CheckLeak(g, 0)

	req := make(chan []byte, 30)
	t.Cleanup(func() { close(req) })

	ws := &MockWebSocket{
		send: func(data []byte) error {
			req <- data
			return nil
		},
		read: func() ([]byte, error) {
			data, ok := <-req
			if !ok {
				return nil, io.EOF
			}

			var req cdp.Request
			err := json.Unmarshal(data, &req)
			if err != nil {
				return nil, err
			}

			return json.Marshal(cdp.Response{
				ID:     req.ID,
				Result: json.RawMessage(jsonvalue.New(req.Params).JSON("", "")),
				Error:  nil,
			})
		},
	}

	c := cdp.New().Start(ws)

	for i := range 1000 {
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			g := setup(t)
			g.Parallel()

			res, err := c.Call(g.Context(), "1234567890", "method", i)
			g.E(err)
			g.Eq(jsonvalue.New(res).Int(), i)
		})
	}
}

type MockWebSocket struct {
	send func(data []byte) error
	read func() ([]byte, error)
}

func (c *MockWebSocket) Send(data []byte) error {
	return c.send(data)
}

func (c *MockWebSocket) Read() ([]byte, error) {
	return c.read()
}
