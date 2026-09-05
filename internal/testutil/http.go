package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Router is a loopback HTTP server owned by a test.
type Router struct {
	g G

	HostURL *url.URL
	Server  *http.Server
	Mux     *http.ServeMux
}

// Serve starts a loopback HTTP server and closes it during test cleanup.
func (g G) Serve() *Router {
	g.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	hostURL, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		g.Fatalf("testutil: parse test server URL: %v", err)
		return nil
	}

	router := &Router{
		g:       g,
		HostURL: hostURL,
		Server:  server.Config,
		Mux:     mux,
	}
	g.Cleanup(server.Close)
	return router
}

// URL returns the server URL with path appended.
func (router *Router) URL(path ...string) string {
	joined := strings.Join(path, "")
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return router.HostURL.String() + joined
}

// Route registers a handler that serves file or writes value.
func (router *Router) Route(pattern, file string, value ...any) *Router {
	router.g.Helper()
	router.Mux.HandleFunc(pattern, router.g.HandleHTTP(file, value...))
	return router
}

// HandleHTTP builds a handler that serves an existing file or a fixed response value.
func (g G) HandleHTTP(file string, value ...any) http.HandlerFunc {
	g.Helper()

	var response any
	switch len(value) {
	case 0:
	case 1:
		response = value[0]
	default:
		response = value
	}

	body, err := responseBytes(response)
	if err != nil {
		g.Fatalf("testutil: encode HTTP response: %v", err)
		return func(http.ResponseWriter, *http.Request) {}
	}
	contentType := mime.TypeByExtension(filepath.Ext(file))

	return func(w http.ResponseWriter, request *http.Request) {
		if file != "" {
			if info, err := os.Stat(file); err == nil && !info.IsDir() {
				http.ServeFile(w, request, file)
				return
			}
		}
		if response == nil {
			return
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if _, err := w.Write(body); err != nil {
			g.Errorf("testutil: write HTTP response: %v", err)
		}
	}
}

// ReqMIME selects a request Content-Type by file extension.
type ReqMIME string

// Req sends an HTTP request and returns a response helper.
// Header, ReqMIME, and context.Context values configure the request;
// another option is encoded as its body.
func (g G) Req(method, rawURL string, options ...any) *ResHelper {
	g.Helper()

	headers := make(http.Header)
	host := ""
	contentType := ""
	ctx := context.Background()
	var body io.Reader

	for _, option := range options {
		switch value := option.(type) {
		case http.Header:
			headers = value.Clone()
			host = headers.Get("Host")
			headers.Del("Host")
		case ReqMIME:
			contentType = mime.TypeByExtension(filepath.Ext(string(value)))
		case context.Context:
			ctx = value
		default:
			encoded, err := responseBytes(value)
			if err != nil {
				return &ResHelper{g: g, err: err}
			}
			body = bytes.NewReader(encoded)
		}
	}

	if method == "" {
		method = http.MethodGet
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return &ResHelper{g: g, err: err}
	}
	request.Header = headers
	request.Host = host
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}

	response, err := http.DefaultClient.Do(request)
	return &ResHelper{g: g, Response: response, err: err}
}

// ResHelper exposes an HTTP response with convenient body decoders.
type ResHelper struct {
	g G
	*http.Response

	err      error
	bodyOnce sync.Once
	body     []byte
	bodyErr  error
}

// Bytes reads and closes the response body, returning a fresh buffer on each call.
func (response *ResHelper) Bytes() *bytes.Buffer {
	response.g.Helper()
	response.readBody()
	if response.err != nil {
		response.g.Fatalf("testutil: HTTP request: %v", response.err)
		return bytes.NewBuffer(nil)
	}
	if response.bodyErr != nil {
		response.g.Fatalf("testutil: read HTTP response: %v", response.bodyErr)
		return bytes.NewBuffer(nil)
	}
	return bytes.NewBuffer(bytes.Clone(response.body))
}

// String reads the response body as a string.
func (response *ResHelper) String() string {
	response.g.Helper()
	return response.Bytes().String()
}

// JSON decodes the response body using encoding/json defaults.
func (response *ResHelper) JSON() (value any) {
	response.g.Helper()
	response.Unmarshal(&value)
	return value
}

// Unmarshal decodes the response body into value using encoding/json defaults.
func (response *ResHelper) Unmarshal(value any) {
	response.g.Helper()
	if err := json.Unmarshal(response.Bytes().Bytes(), value); err != nil {
		response.g.Fatalf("testutil: decode HTTP response: %v", err)
	}
}

// Err returns the request or body-read error, if one has occurred.
func (response *ResHelper) Err() error {
	return errors.Join(response.err, response.bodyErr)
}

func (response *ResHelper) readBody() {
	response.bodyOnce.Do(func() {
		if response.err != nil {
			return
		}
		if response.Response == nil || response.Body == nil {
			response.bodyErr = ErrMissingResponseBody
			return
		}

		response.body, response.bodyErr = io.ReadAll(response.Body)
		response.bodyErr = errors.Join(response.bodyErr, response.Body.Close())
	})
}

func responseBytes(value any) ([]byte, error) {
	switch value := value.(type) {
	case nil:
		return nil, nil
	case []byte:
		return bytes.Clone(value), nil
	case string:
		return []byte(value), nil
	case io.Reader:
		return io.ReadAll(value)
	default:
		buffer := bytes.NewBuffer(nil)
		err := json.NewEncoder(buffer).Encode(value)
		return buffer.Bytes(), err
	}
}
