package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rah-0/rod/lib/utils"
)

var _ io.Writer = &URLParser{}

const maxBrowserOutput = 64 * 1024

// URLParser to get control url from stderr.
type URLParser struct {
	URL    chan string
	Buffer string // buffer for the browser stdout

	lock *sync.Mutex
	ctx  context.Context
	done bool
}

// NewURLParser instance.
func NewURLParser() *URLParser {
	return &URLParser{
		URL:  make(chan string, 1),
		lock: &sync.Mutex{},
		ctx:  context.Background(),
	}
}

var regWS = regexp.MustCompile(`ws://.+/`)

// Context sets the context.
func (r *URLParser) Context(ctx context.Context) *URLParser {
	r.ctx = ctx
	return r
}

// Write interface.
func (r *URLParser) Write(p []byte) (n int, err error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	if !r.done {
		// Retain only recent startup diagnostics if the browser never advertises
		// DevTools. A failing, noisy browser must not grow memory without bound.
		if len(p) >= maxBrowserOutput {
			r.Buffer = string(p[len(p)-maxBrowserOutput:])
		} else {
			if excess := len(r.Buffer) + len(p) - maxBrowserOutput; excess > 0 {
				r.Buffer = r.Buffer[excess:]
			}
			r.Buffer += string(p)
		}

		str := regWS.FindString(r.Buffer)
		if str != "" {
			u, err := url.Parse(strings.TrimSpace(str))
			utils.E(err)

			select {
			case <-r.ctx.Done():
			case r.URL <- "http://" + u.Host:
			}

			r.done = true
			r.Buffer = ""
		}
	}

	return len(p), nil
}

// Err returns the common error parsed from stdout and stderr.
func (r *URLParser) Err() error {
	r.lock.Lock()
	defer r.lock.Unlock()

	msg := "[launcher] Failed to get the debug url: "

	if strings.Contains(r.Buffer, "error while loading shared libraries") {
		msg = "[launcher] Failed to launch the browser: "
	}

	return errors.New(msg + r.Buffer)
}

// MustResolveURL is similar to ResolveURL.
func MustResolveURL(ctx context.Context, u string) string {
	u, err := ResolveURL(ctx, u)
	utils.E(err)
	return u
}

var (
	regPort     = regexp.MustCompile(`^\:?(\d+)$`)
	regProtocol = regexp.MustCompile(`^\w+://`)
)

// ResolveURL by requesting the u, it will try best to normalize the u.
// The format of u can be "9222", ":9222", "host:9222", "ws://host:9222", "wss://host:9222",
// "https://host:9222" "http://host:9222". The return string will look like:
// "ws://host:9222/devtools/browser/4371405f-84df-4ad6-9e0f-eab81f7521cc"
// Discovery requests honor ctx and have a maximum duration of 10 seconds,
// including reading the response body.
func ResolveURL(ctx context.Context, u string) (string, error) {
	if u == "" {
		u = "9222"
	}

	u = strings.TrimSpace(u)
	u = regPort.ReplaceAllString(u, "127.0.0.1:$1")

	if !regProtocol.MatchString(u) {
		u = "http://" + u
	}

	parsed, err := url.Parse(u)
	if err != nil {
		return "", err
	}

	parsed = toHTTP(*parsed)
	parsed.Path = "/json/version"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolve browser URL: HTTP status %s", res.Status)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("read browser discovery: %w", err)
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(data, &version); err != nil {
		return "", fmt.Errorf("decode browser discovery: %w", err)
	}

	parsedWS, err := url.Parse(version.WebSocketDebuggerURL)
	if err != nil {
		return "", fmt.Errorf("parse browser WebSocket URL: %w", err)
	}
	if (parsedWS.Scheme != "ws" && parsedWS.Scheme != "wss") || parsedWS.Host == "" {
		return "", fmt.Errorf("invalid browser WebSocket URL: %q", version.WebSocketDebuggerURL)
	}

	parsedWS.Host = parsed.Host

	return parsedWS.String(), nil
}
