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
	"math"
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

const (
	// DefaultHandshakeTimeout bounds WebSocket.Connect when
	// WebSocket.HandshakeTimeout is zero.
	DefaultHandshakeTimeout = 30 * time.Second

	// DefaultMaxMessageSize limits one incoming message when
	// WebSocket.MaxMessageSize is not positive. It accommodates large
	// screenshots, PDFs, and evaluation results.
	DefaultMaxMessageSize = 256 << 20

	// maxHandshakeResponse bounds the bytes Connect reads before the upgrade
	// completes, using the net/http default for request headers. Chrome's
	// response has only a few hundred bytes.
	maxHandshakeResponse = http.DefaultMaxHeaderBytes

	// maxPayloadPrealloc bounds the bytes Read allocates for a frame before its
	// payload arrives, whatever MaxMessageSize allows. Messages up to the
	// default limit are allocated once at their exact size.
	maxPayloadPrealloc = DefaultMaxMessageSize
)

// WebSocket carries CDP text messages, including fragmented messages and control frames.
// Read and Send are safe for concurrent use. Compression and binary messages are not supported.
// Limitation: https://bugs.chromium.org/p/chromium/issues/detail?id=1069431
// Ref: https://tools.ietf.org/html/rfc6455
type WebSocket struct {
	// Dialer is usually used for proxy. Its context can end when Connect
	// returns, so the dialed connection must not depend on that context, as
	// with net.Dialer.
	Dialer Dialer

	// HandshakeTimeout bounds dialing, TLS, and the HTTP upgrade in Connect,
	// in addition to Connect's context. Zero selects DefaultHandshakeTimeout;
	// a negative value leaves only the context in effect. It does not limit
	// the connection after Connect returns.
	HandshakeTimeout time.Duration

	// MaxMessageSize limits the bytes of one message read from the peer,
	// including all of its fragments. Zero or a negative value selects
	// DefaultMaxMessageSize, and math.MaxInt64 accepts any size. A larger
	// message fails Read with ErrWebSocketMessageTooLarge and closes the
	// connection with status 1009. Set it before the first Read.
	//
	// Read allocates a frame's payload as soon as its header announces the
	// length, up to DefaultMaxMessageSize bytes. Longer payloads grow as their
	// bytes arrive, so a header alone cannot force a larger allocation.
	MaxMessageSize int64

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
// HandshakeTimeout also bounds establishment, and at most 1 MiB of handshake
// response is read.
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
	parent := ctx
	handshakeTimeout := ws.HandshakeTimeout
	if handshakeTimeout == 0 {
		handshakeTimeout = DefaultHandshakeTimeout
	}
	if handshakeTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, handshakeTimeout,
			fmt.Errorf("WebSocket handshake exceeded %s: %w", handshakeTimeout, context.DeadlineExceeded))
		defer cancel()
	}
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
	limit := &handshakeReader{conn: conn, remaining: maxHandshakeResponse}
	ws.r = bufio.NewReader(limit)
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
	if deadline, ok := ctx.Deadline(); ok && err != nil && !time.Now().Before(deadline) {
		// The connection deadline can fire before the context's timer callback.
		if timeout, ok := errors.AsType[net.Error](err); ok && timeout.Timeout() {
			<-ctx.Done()
		}
	}
	if parent.Err() != nil {
		err = parent.Err()
	} else if ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	if err == nil {
		limit.remaining = -1
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
// writing. If cancellation interrupts a write before the connection accepts any
// of the frame, the frame is abandoned and the connection remains usable. A
// partially written frame cannot be resumed by another request, so that failure
// closes the connection. TLS connections close after any failed write because
// TLS rejects every later write.
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
	frame, err := clientFrame(opcode, msg)
	if err != nil {
		return err
	}
	// Building a large frame takes time. Leave the stream untouched if the
	// operation ended meanwhile.
	if err := ctx.Err(); err != nil {
		return err
	}
	if ctx.Done() == nil {
		n, err := ws.write(frame)
		if err != nil && ws.writeBroken(n, err) {
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
	n, err := ws.write(frame)
	if !stop() {
		<-interrupted
	}
	if err != nil && ws.writeBroken(n, err) {
		_ = ws.Close()
	} else if reset := ws.conn.SetWriteDeadline(time.Time{}); err == nil {
		err = reset
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

func (ws *WebSocket) write(frame []byte) (int, error) {
	n, err := ws.conn.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	return n, err
}

// writeBroken reports whether a failed write of n bytes leaves the stream
// unusable. A timeout before the connection accepts any byte abandons only the
// frame. A partial frame cannot be completed, TLS rejects all writes after a
// failed one, and other write errors indicate a failed connection.
func (ws *WebSocket) writeBroken(n int, err error) bool {
	if _, secure := ws.conn.(interface{ ConnectionState() tls.ConnectionState }); secure {
		return true
	}
	timeout, ok := errors.AsType[net.Error](err)
	return n != 0 || !ok || !timeout.Timeout()
}

// clientFrame encodes msg as one final frame with a fresh random mask.
func clientFrame(opcode byte, msg []byte) ([]byte, error) {
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
		return nil, err
	}
	maskBytes(data[headerSize:], msg, [4]byte(mask))
	return data, nil
}

// maskBytes stores src XOR the repeating mask in dst, eight bytes at a time.
func maskBytes(dst, src []byte, mask [4]byte) {
	word := uint64(binary.LittleEndian.Uint32(mask[:]))
	word |= word << 32
	i := 0
	for ; i+8 <= len(src); i += 8 {
		binary.LittleEndian.PutUint64(dst[i:], binary.LittleEndian.Uint64(src[i:])^word)
	}
	for ; i < len(src); i++ {
		dst[i] = src[i] ^ mask[i%4]
	}
}

// Read a message from browser. A message larger than MaxMessageSize returns
// ErrWebSocketMessageTooLarge. Every error closes the connection.
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
	limit := uint64(DefaultMaxMessageSize)
	if ws.MaxMessageSize > 0 {
		limit = min(uint64(ws.MaxMessageSize), math.MaxInt)
	}
	var message []byte
	fragmented := false
	for {
		header, err := ws.readHeader(2)
		if err != nil {
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
			extended, err := ws.readHeader(2)
			if err != nil {
				return nil, err
			}
			size = uint64(binary.BigEndian.Uint16(extended))
			if size < 126 {
				return nil, fmt.Errorf("%w: nonminimal frame length", ErrWebSocketProtocol)
			}
		case 127:
			extended, err := ws.readHeader(8)
			if err != nil {
				return nil, err
			}
			size = binary.BigEndian.Uint64(extended)
			if size <= 65535 || size>>63 != 0 {
				return nil, fmt.Errorf("%w: invalid frame length", ErrWebSocketProtocol)
			}
		}
		if opcode < 8 {
			// Reject an oversized message before allocating its announced length.
			if size > limit-uint64(len(message)) {
				err := fmt.Errorf("%w: at least %d bytes exceed the %d-byte limit",
					ErrWebSocketMessageTooLarge, uint64(len(message))+size, limit)
				return nil, errors.Join(err, ws.replyControl(8, []byte{0x03, 0xf1})) // 1009
			}
			message, err = appendPayload(ws.r, message, int(size), maxPayloadPrealloc)
			if err != nil {
				return nil, err
			}
			if fin {
				if !utf8.Valid(message) {
					return nil, fmt.Errorf("%w: invalid UTF-8 text", ErrWebSocketProtocol)
				}
				return message, nil
			}
			fragmented = true
			continue
		}
		var control [125]byte
		payload := control[:size]
		if err := readPayload(ws.r, payload); err != nil {
			return nil, err
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
		}
	}
}

// readHeader consumes n header bytes, returning a view of the read buffer that
// is valid until the next read. Unlike io.ReadFull, it does not allocate.
func (ws *WebSocket) readHeader(n int) ([]byte, error) {
	header, err := ws.r.Peek(n)
	if len(header) < n {
		if len(header) > 0 && errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	_, _ = ws.r.Discard(n)
	return header, nil
}

// appendPayload reads size payload bytes from r and appends them to message.
// Each step allocates for at most prealloc bytes, or for as many bytes as the
// message already holds if that is more, and fills them before growing again.
// A payload up to prealloc bytes is therefore allocated at once, while a longer
// one requires the peer to send data before the allocation grows further.
func appendPayload(r io.Reader, message []byte, size, prealloc int) ([]byte, error) {
	end := len(message) + size
	for message == nil || len(message) < end {
		start := len(message)
		n := min(end-start, max(prealloc, start))
		if message == nil {
			message = make([]byte, n)
		} else {
			message = slices.Grow(message, n)[:start+n]
		}
		if err := readPayload(r, message[start:]); err != nil {
			return nil, err
		}
	}
	return message, nil
}

// readPayload fills payload, reporting a truncated frame as unexpected EOF.
func readPayload(r io.Reader, payload []byte) error {
	_, err := io.ReadFull(r, payload)
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
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

// handshakeReader limits the bytes read from conn until remaining is negative.
type handshakeReader struct {
	conn      io.Reader
	remaining int
}

func (r *handshakeReader) Read(p []byte) (int, error) {
	if r.remaining < 0 {
		return r.conn.Read(p)
	}
	if r.remaining == 0 {
		return 0, fmt.Errorf("%w: handshake response exceeds %d bytes", ErrWebSocketProtocol, maxHandshakeResponse)
	}
	n, err := r.conn.Read(p[:min(len(p), r.remaining)])
	r.remaining -= n
	return n, err
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
