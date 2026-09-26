package cdp

import (
	"context"
	"net"
	"net/http"

	"github.com/rah-0/rod/lib/utils"
)

// Dialer interface for WebSocket connection. As with net.Dialer, ctx bounds
// only the dial. WebSocket.Connect can cancel it when Connect returns, and the
// returned connection must remain usable afterward.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// MustConnectWS helper to make a websocket connection.
func MustConnectWS(wsURL string) WebSocketable {
	ws := &WebSocket{}
	utils.E(ws.Connect(context.Background(), wsURL, nil))
	return ws
}

// MustStartWithURL helper for ConnectURL.
func MustStartWithURL(ctx context.Context, u string, h http.Header) *Client {
	c, err := StartWithURL(ctx, u, h)
	utils.E(err)
	return c
}

// StartWithURL helper to connect to the u with the default websocket lib.
func StartWithURL(ctx context.Context, u string, h http.Header) (*Client, error) {
	ws := &WebSocket{}
	err := ws.Connect(ctx, u, h)
	if err != nil {
		return nil, err
	}
	return New().Start(ws), nil
}
