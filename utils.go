package rod

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// CDPClient is usually used to make rod side-effect free. Such as proxy all IO of rod.
type CDPClient interface {
	Event() <-chan *cdp.Event
	Call(ctx context.Context, sessionID, method string, params any) ([]byte, error)
}

// Message represents a cdp.Event. Do not copy it after first use.
type Message struct {
	SessionID proto.TargetSessionID
	Method    string

	lock  sync.Mutex
	data  json.RawMessage
	event any
}

// lenientEvent is the event of a [Message] that Load decodes with
// [proto.DecodeLenient]. It holds the last decoded event, or nil. Keeping the
// mode in the event field instead of a new field keeps each message in a
// smaller allocation size class.
type lenientEvent struct{ event any }

// newMessage returns the message of an event that a browser with the given
// decoding received.
func newMessage(session proto.TargetSessionID, method string, data json.RawMessage, decoding proto.Decoding) *Message {
	msg := &Message{SessionID: session, Method: method, data: data}
	if decoding == proto.DecodeLenient {
		msg.event = lenientEvent{}
	}
	return msg
}

// Load decodes a matching event into out and reports whether out holds it.
// E is the concrete protocol event type. A message for another method returns
// false and a nil error. When the method matches but the parameters cannot be
// decoded as E, Load returns false and the decoding error, leaving out unchanged.
// Parameters are decoded with the [Browser.Decoding] of the browser that
// received the event, [proto.DecodeStrict] by default, which returns an error
// that wraps [*proto.MissingFieldError] for an event that lacks a field the
// protocol requires.
// Subscribers loading the same type share a cached decode and receive a shallow
// copy. Treat referenced data, such as slices and nested pointers, as read-only.
func (msg *Message) Load[E proto.Event](out *E) (bool, error) {
	if msg.Method != (*out).ProtoEvent() {
		return false, nil
	}

	msg.lock.Lock()
	defer msg.lock.Unlock()
	cached := msg.event
	marked, lenient := cached.(lenientEvent)
	if lenient {
		cached = marked.event
	}
	if event, ok := cached.(E); ok {
		*out = event
		return true, nil
	}
	decoding := proto.DecodeStrict
	if lenient {
		decoding = proto.DecodeLenient
	}
	var decoded E
	if err := decoding.Unmarshal(msg.data, &decoded); err != nil {
		return false, fmt.Errorf("rod: decode %s event: %w", msg.Method, err)
	}
	// Loading another type replaces the cached event but keeps the mode.
	if lenient {
		msg.event = lenientEvent{decoded}
	} else {
		msg.event = decoded
	}
	*out = decoded
	return true, nil
}

// DefaultLogger for rod.
var DefaultLogger = log.New(os.Stdout, "[rod] ", log.LstdFlags)

// DefaultSleeper creates a retry sleeper with a 10 ms seed and exponential backoff:
//
//	A(0) = 10ms, A(n) = min(A(n-1) * random[1.9, 2.1), 1s)
//
// The first sleep is approximately 19–21 ms. Increasing the interval limits
// repeated evaluations during longer waits. Use a custom sleeper to choose a
// different retry cadence.
var DefaultSleeper = func() utils.Sleeper {
	return utils.BackoffSleeper(10*time.Millisecond, time.Second, nil)
}

// NewPagePool instance.
func NewPagePool(limit int) Pool[Page] {
	return NewPool[Page](limit)
}

// NewBrowserPool instance.
func NewBrowserPool(limit int) Pool[Browser] {
	return NewPool[Browser](limit)
}

// Pool is used to thread-safely limit the number of elements at the same time.
// It's a common practice to use a channel to limit concurrency, it's not special for rod.
// This helper is more like an example to use Go Channel.
// Reference: https://golang.org/doc/effective_go#channels
type Pool[T any] chan *T

// NewPool instance.
func NewPool[T any](limit int) Pool[T] {
	p := make(chan *T, limit)
	for range limit {
		p <- nil
	}
	return p
}

// Get a elem from the pool, allow error. Use the [Pool[T].Put] to make it reusable later.
func (p Pool[T]) Get(create func() (*T, error)) (elem *T, err error) {
	elem = <-p
	if elem == nil {
		elem, err = create()
	}
	return
}

// Put an elem back to the pool.
func (p Pool[T]) Put(elem *T) {
	p <- elem
}

// Cleanup helper.
func (p Pool[T]) Cleanup(iteratee func(*T)) {
	for range cap(p) {
		select {
		case elem := <-p:
			if elem != nil {
				iteratee(elem)
			}
		default:
		}
	}
}

var _ io.ReadCloser = &StreamReader{}

// pdfChunkSize is the StreamReader.ChunkSize of readers returned by Page.PDF.
const pdfChunkSize = 4 << 20

// StreamReader for browser data stream.
type StreamReader struct {
	// Offset, when non-nil, is the stream position of the next byte Read returns.
	// Read advances it by the number of bytes returned. Assigning Offset or
	// changing its value before a Read discards buffered data and reads from there.
	Offset *int

	// ChunkSize is the minimum number of bytes each IO.read requests. Read
	// requests the larger of ChunkSize and len(p) and keeps data beyond p for
	// later reads, saving round trips for small buffers. The browser answers at
	// once for complete data, such as PDF output or a blob, but for a response
	// body still loading, such as a Fetch.takeResponseBodyAsStream stream, only
	// when the requested size arrives or the body ends. Each response encodes up
	// to the requested size as base64 in one CDP message. Zero requests len(p).
	ChunkSize int

	c      proto.Client
	handle proto.IOStreamHandle
	buf    []byte // received data not yet returned by Read
	pos    int    // stream position of buf[0]
	synced *int   // Offset whose value matches pos
	eof    bool
}

// NewStreamReader instance.
func NewStreamReader(c proto.Client, h proto.IOStreamHandle) *StreamReader {
	return &StreamReader{
		c:      c,
		handle: h,
	}
}

func (sr *StreamReader) Read(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	if sr.Offset != nil && (sr.Offset != sr.synced || *sr.Offset != sr.pos) {
		sr.buf, sr.pos, sr.eof = nil, *sr.Offset, false
	}
	sr.synced = sr.Offset
	if len(sr.buf) == 0 && !sr.eof {
		if err := sr.fill(max(len(p), sr.ChunkSize)); err != nil {
			return 0, err
		}
	}
	if len(sr.buf) == 0 {
		if sr.eof {
			return 0, io.EOF
		}
		return 0, nil
	}
	n = copy(p, sr.buf)
	sr.buf = sr.buf[n:]
	if len(sr.buf) == 0 {
		sr.buf = nil
	}
	sr.pos += n
	if sr.Offset != nil {
		*sr.Offset = sr.pos
	}
	return n, nil
}

// fill replaces the empty buffer with up to size bytes of the stream.
func (sr *StreamReader) fill(size int) error {
	req := proto.IORead{Handle: sr.handle, Size: new(size)}
	if sr.Offset != nil {
		req.Offset = new(sr.pos)
	}
	res, err := req.Call(sr.c)
	if err != nil {
		return err
	}

	var bin []byte
	if res.Base64Encoded {
		bin, err = base64.StdEncoding.DecodeString(res.Data)
		if err != nil {
			return err
		}
	} else {
		bin = []byte(res.Data)
	}
	if len(bin) == 0 && !res.EOF {
		// Lenient decoding leaves a result without data and eof empty and
		// false, and reading on would never reach the end.
		if c, ok := sr.c.(proto.Decodable); ok && c.GetDecoding() == proto.DecodeLenient {
			return missingField("IOReadResult", "eof")
		}
	}
	sr.buf, sr.eof = bin, res.EOF
	return nil
}

// Close the stream, discard any temporary backing storage.
func (sr *StreamReader) Close() error {
	return proto.IOClose{Handle: sr.handle}.Call(sr.c)
}

// Try try fn with recover, return the panic as rod.ErrTry.
func Try(fn func()) (err error) {
	defer func() {
		if val := recover(); val != nil {
			err = &TryError{val, string(debug.Stack())}
		}
	}()

	fn()

	return err
}

func genRegMatcher(includes, excludes []string) func(string) bool {
	regIncludes := make([]*regexp.Regexp, len(includes))
	for i, p := range includes {
		regIncludes[i] = regexp.MustCompile(p)
	}

	regExcludes := make([]*regexp.Regexp, len(excludes))
	for i, p := range excludes {
		regExcludes[i] = regexp.MustCompile(p)
	}

	return func(s string) bool {
		for _, include := range regIncludes {
			if include.MatchString(s) {
				for _, exclude := range regExcludes {
					if exclude.MatchString(s) {
						goto end
					}
				}
				return true
			}
		}
	end:
		return false
	}
}

type saveFileType int

const (
	saveFileTypeScreenshot saveFileType = iota
	saveFileTypePDF
)

func saveFile(fileType saveFileType, bin []byte, toFile []string) error {
	if len(toFile) == 0 {
		return nil
	}
	if toFile[0] == "" {
		stamp := fmt.Sprintf("%d", time.Now().UnixNano())
		switch fileType {
		case saveFileTypeScreenshot:
			toFile = []string{"tmp", "screenshots", stamp + ".png"}
		case saveFileTypePDF:
			toFile = []string{"tmp", "pdf", stamp + ".pdf"}
		}
	}
	return utils.OutputFile(filepath.Join(toFile...), bin)
}

func httHTML(w http.ResponseWriter, body string) {
	w.Header().Add("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

func mustToJSONForDev(value any) string {
	buf := new(bytes.Buffer)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)

	utils.E(enc.Encode(value))

	return buf.String()
}
