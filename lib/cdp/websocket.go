package cdp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var _ WebSocketable = &WebSocket{}

// WebSocket carries CDP text messages, including fragmented messages and control frames.
// Read and Send are safe for concurrent use. Compression and binary messages are not supported.
// Limitation: https://bugs.chromium.org/p/chromium/issues/detail?id=1069431
// Ref: https://tools.ietf.org/html/rfc6455
type WebSocket struct {
	// Dialer is usually used for proxy
	Dialer Dialer

	closeOnce sync.Once
	closeErr  error
	writeOnce sync.Once
	writeLock chan struct{}
	lock      sync.Mutex
	conn      net.Conn
	r         *bufio.Reader
}

// Connect to browser. The context controls connection establishment only;
// canceling it after Connect succeeds does not close the connection.
func (ws *WebSocket) Connect(ctx context.Context, wsURL string, header http.Header) error {
	if ws.conn != nil {
		panic("duplicated connection: " + wsURL)
	}

	u, err := url.Parse(wsURL)
	if err != nil {
		return err
	}

	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return fmt.Errorf("%w: unsupported URL scheme %q", ErrWebSocketProtocol, u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%w: missing host", ErrWebSocketProtocol)
	}
	ws.initDialer(u)
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "wss" {
			port = "443"
		}
	}
	address := net.JoinHostPort(u.Hostname(), port)
	conn, err := ws.Dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return err
		}
	}

	ws.conn = conn
	ws.r = bufio.NewReader(conn)
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.Close()
		close(canceled)
	})
	err = ws.handshake(ctx, u, header)
	if !stop() {
		// Wait for a callback that already started before inspecting or reusing conn.
		<-canceled
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		// The connection deadline can fire before the context's timer callback.
		if timeout, ok := errors.AsType[net.Error](err); ok && timeout.Timeout() {
			err = context.DeadlineExceeded
		}
	}
	if err == nil {
		err = conn.SetDeadline(time.Time{})
	}
	if err != nil {
		_ = conn.Close()
		ws.conn = nil
		ws.r = nil
	}
	return err
}

// Close the underlying connection.
func (ws *WebSocket) Close() error {
	if ws.conn == nil {
		return nil
	}
	ws.closeOnce.Do(func() {
		ws.closeErr = ws.conn.Close()
		if errors.Is(ws.closeErr, net.ErrClosed) {
			ws.closeErr = nil
		}
	})
	return ws.closeErr
}

func (ws *WebSocket) initDialer(u *url.URL) {
	if ws.Dialer != nil {
		return
	}

	if u.Scheme == "wss" {
		ws.Dialer = &tls.Dialer{}
	} else {
		ws.Dialer = &net.Dialer{}
	}
}

// Send a message to browser.
// The payload is preserved. Each client frame uses a fresh random mask.
func (ws *WebSocket) Send(msg []byte) error {
	return ws.SendContext(context.Background(), msg)
}

// SendContext sends one frame, honoring cancellation while waiting to write or
// writing. An interrupted write closes the connection because a partial frame
// cannot safely be resumed by another request.
func (ws *WebSocket) SendContext(ctx context.Context, msg []byte) error {
	if !utf8.Valid(msg) {
		return fmt.Errorf("%w: invalid UTF-8 text", ErrWebSocketProtocol)
	}
	return ws.writeFrame(ctx, 1, msg)
}

func (ws *WebSocket) writeFrame(ctx context.Context, opcode byte, msg []byte) error {
	if ws.conn == nil {
		return io.ErrClosedPipe
	}
	ws.writeOnce.Do(func() { ws.writeLock = make(chan struct{}, 1) })
	select {
	case ws.writeLock <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-ws.writeLock }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if ctx.Done() == nil {
		err := ws.send(opcode, msg)
		if err != nil {
			_ = ws.Close()
		}
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := ws.conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = ws.conn.SetWriteDeadline(time.Now())
		close(interrupted)
	})
	err := ws.send(opcode, msg)
	if !stop() {
		<-interrupted
	}
	if err != nil {
		_ = ws.Close()
	} else {
		err = ws.conn.SetWriteDeadline(time.Time{})
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		if timeout, ok := errors.AsType[net.Error](err); ok && timeout.Timeout() {
			return context.DeadlineExceeded
		}
	}
	return err
}

func (ws *WebSocket) send(opcode byte, msg []byte) error {
	headerSize := 6 // two fixed bytes plus the client mask
	if len(msg) > 65535 {
		headerSize += 8
	} else if len(msg) > 125 {
		headerSize += 2
	}
	data := make([]byte, headerSize+len(msg))
	data[0] = 0x80 | opcode
	data[1] = 0x80
	switch {
	case len(msg) <= 125:
		data[1] |= byte(len(msg))
	case len(msg) <= 65535:
		data[1] |= 126
		binary.BigEndian.PutUint16(data[2:], uint16(len(msg)))
	default:
		data[1] |= 127
		binary.BigEndian.PutUint64(data[2:], uint64(len(msg)))
	}
	mask := data[headerSize-4 : headerSize]
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	for i, value := range msg {
		data[headerSize+i] = value ^ mask[i%4]
	}
	n, err := ws.conn.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

// Read a message from browser.
func (ws *WebSocket) Read() ([]byte, error) {
	b, err := ws.read()
	if err != nil {
		_ = ws.Close()
		return nil, err
	}
	return b, nil
}

func (ws *WebSocket) read() ([]byte, error) {
	ws.lock.Lock()
	defer ws.lock.Unlock()
	var message []byte
	fragmented := false
	for {
		var header [2]byte
		if _, err := io.ReadFull(ws.r, header[:]); err != nil {
			return nil, err
		}
		fin, opcode := header[0]&0x80 != 0, header[0]&0x0f
		if header[0]&0x70 != 0 || header[1]&0x80 != 0 {
			return nil, fmt.Errorf("%w: reserved bits or masked server frame", ErrWebSocketProtocol)
		}
		if opcode != 0 && opcode != 1 && opcode != 8 && opcode != 9 && opcode != 10 {
			return nil, fmt.Errorf("%w: unsupported opcode %d", ErrWebSocketProtocol, opcode)
		}
		if (opcode == 0 && !fragmented) || (opcode == 1 && fragmented) {
			return nil, fmt.Errorf("%w: invalid message fragmentation", ErrWebSocketProtocol)
		}
		size := uint64(header[1] & 0x7f)
		if opcode >= 8 && (!fin || size > 125) {
			return nil, fmt.Errorf("%w: invalid control frame", ErrWebSocketProtocol)
		}
		switch size {
		case 126:
			var extended [2]byte
			if _, err := io.ReadFull(ws.r, extended[:]); err != nil {
				return nil, err
			}
			size = uint64(binary.BigEndian.Uint16(extended[:]))
			if size < 126 {
				return nil, fmt.Errorf("%w: nonminimal frame length", ErrWebSocketProtocol)
			}
		case 127:
			var extended [8]byte
			if _, err := io.ReadFull(ws.r, extended[:]); err != nil {
				return nil, err
			}
			size = binary.BigEndian.Uint64(extended[:])
			if size <= 65535 || size>>63 != 0 {
				return nil, fmt.Errorf("%w: invalid frame length", ErrWebSocketProtocol)
			}
		}
		if size > uint64(int(^uint(0)>>1))-uint64(len(message)) {
			return nil, fmt.Errorf("%w: message exceeds addressable size", ErrWebSocketProtocol)
		}
		// Grow only as bytes arrive, never allocate the peer's advertised length.
		payload, err := io.ReadAll(io.LimitReader(ws.r, int64(size)))
		if err != nil {
			return nil, err
		}
		if uint64(len(payload)) != size {
			return nil, io.ErrUnexpectedEOF
		}
		switch opcode {
		case 8:
			closed := &WebSocketCloseError{Code: 1005}
			if len(payload) == 1 {
				return nil, fmt.Errorf("%w: incomplete close status", ErrWebSocketProtocol)
			}
			if len(payload) >= 2 {
				closed.Code = binary.BigEndian.Uint16(payload)
				closed.Reason = string(payload[2:])
				code := closed.Code
				if !((code >= 1000 && code <= 1014 && code != 1004 && code != 1005 && code != 1006) || (code >= 3000 && code <= 4999)) || !utf8.ValidString(closed.Reason) {
					return nil, fmt.Errorf("%w: invalid close status or reason", ErrWebSocketProtocol)
				}
			}
			return nil, errors.Join(closed, ws.replyControl(8, payload))
		case 9:
			if err := ws.replyControl(10, payload); err != nil {
				return nil, err
			}
			continue
		case 10:
			continue
		}
		if message == nil {
			message = payload
		} else {
			message = append(message, payload...)
		}
		if fin {
			if !utf8.Valid(message) {
				return nil, fmt.Errorf("%w: invalid UTF-8 text", ErrWebSocketProtocol)
			}
			return message, nil
		}
		fragmented = true
	}
}

func (ws *WebSocket) replyControl(opcode byte, payload []byte) error {
	// Read has no operation context. Bound its protocol response so an
	// unresponsive peer cannot prevent Close delivery or hold this reader forever.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return ws.writeFrame(ctx, opcode, payload)
}

// BadHandshakeError type.
type BadHandshakeError struct {
	Status string
	Body   string
}

func (e *BadHandshakeError) Error() string {
	return fmt.Sprintf(
		"websocket bad handshake: %s. %s",
		e.Status, e.Body,
	)
}

func verifyWebSocketAccept(responseHeaders http.Header, websocketKey string) bool {
	expectedKey := websocketKey + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	hash := sha1.New()
	hash.Write([]byte(expectedKey))
	expectedAccept := base64.StdEncoding.EncodeToString(hash.Sum(nil))

	return responseHeaders.Get("Sec-WebSocket-Accept") == expectedAccept
}

func (ws *WebSocket) handshake(ctx context.Context, u *url.URL, header http.Header) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	req := (&http.Request{Method: http.MethodGet, URL: u, Header: make(http.Header)}).WithContext(ctx)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString(nonce[:]))
	req.Header.Set("Sec-WebSocket-Version", "13")
	// Canonicalize before applying overrides so differently cased map entries
	// cannot create duplicate protocol headers or select a different accept key.
	canonical := make(http.Header)
	for name, values := range header {
		name = http.CanonicalHeaderKey(name)
		if previous, exists := canonical[name]; exists && !slices.Equal(previous, values) {
			return fmt.Errorf("%w: conflicting header %q", ErrWebSocketProtocol, name)
		}
		canonical[name] = slices.Clone(values)
	}
	for name, values := range canonical {
		if name == "Host" {
			if len(values) != 1 {
				return fmt.Errorf("%w: invalid Host override", ErrWebSocketProtocol)
			}
			req.Host = values[0]
		} else {
			req.Header[name] = values
		}
	}
	keys := req.Header.Values("Sec-WebSocket-Key")
	if len(keys) != 1 {
		return fmt.Errorf("%w: expected one handshake key", ErrWebSocketProtocol)
	}
	decoded, err := base64.StdEncoding.DecodeString(keys[0])
	if err != nil || len(decoded) != 16 {
		return fmt.Errorf("%w: handshake key must encode 16 bytes", ErrWebSocketProtocol)
	}
	if !headerToken(req.Header, "Upgrade", "websocket") || !headerToken(req.Header, "Connection", "upgrade") ||
		len(req.Header.Values("Sec-WebSocket-Version")) != 1 || req.Header.Get("Sec-WebSocket-Version") != "13" || req.Header.Get("Sec-WebSocket-Extensions") != "" {
		return fmt.Errorf("%w: invalid or unsupported handshake options", ErrWebSocketProtocol)
	}
	if err := req.Write(ws.conn); err != nil {
		return err
	}
	res, err := http.ReadResponse(ws.r, req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	protocol := res.Header.Get("Sec-WebSocket-Protocol")
	protocolOK := protocol == ""
	for _, offered := range req.Header.Values("Sec-WebSocket-Protocol") {
		for item := range strings.SplitSeq(offered, ",") {
			if strings.TrimSpace(item) == protocol {
				protocolOK = true
			}
		}
	}
	if res.StatusCode != http.StatusSwitchingProtocols || len(res.Header.Values("Sec-WebSocket-Accept")) != 1 || !verifyWebSocketAccept(res.Header, keys[0]) ||
		!protocolOK || len(res.Header.Values("Sec-WebSocket-Protocol")) > 1 ||
		!headerToken(res.Header, "Upgrade", "websocket") || !headerToken(res.Header, "Connection", "upgrade") || res.Header.Get("Sec-WebSocket-Extensions") != "" {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		// Body.Close drains unread bytes. Stop the failed connection first so a
		// stalled response cannot retain the handshake beyond the body limit.
		_ = ws.conn.Close()
		return &BadHandshakeError{Status: res.Status, Body: string(body)}
	}
	return nil
}

func headerToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for part := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
