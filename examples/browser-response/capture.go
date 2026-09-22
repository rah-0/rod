package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/cdp"
	"github.com/rah-0/rod/lib/proto"
)

type capturedResponse struct {
	URL    string
	Status int
	Body   []byte
}

var errIncompleteBody = errors.New("response body read completion is unknown")

// captureResponse owns Fetch exclusively on a dedicated page until trigger and
// the first matching non-redirect response finish. Trigger must use its supplied
// page context so cancellation can stop any outstanding protocol calls.
func captureResponse(page *rod.Page, url string, trigger func(*rod.Page) error) (result *capturedResponse, err error) {
	if err := context.Cause(page.GetContext()); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(page.GetContext())
	defer cancel()
	page = page.Context(ctx)

	// Subscribe before enable and before triggering the browser request. Keep
	// this subscription alive until the owned Fetch configuration is disabled.
	listenCtx, stopListening := context.WithCancel(context.WithoutCancel(ctx))
	events := page.Context(listenCtx).Event()
	discardPage := false
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if discardPage {
			// A locally timed-out CDP body read may still be running in Chrome.
			// Close this dedicated target instead of racing it with Fetch calls.
			closed, closeErr := (proto.TargetCloseTarget{TargetID: page.TargetID}).Call(page.Browser().Context(cleanup))
			if closeErr == nil && !closed.Success {
				closeErr = errors.New("browser did not close the dedicated capture page")
			}
			err = errors.Join(err, closeErr)
		} else {
			err = errors.Join(err, (proto.FetchDisable{}).Call(page.Context(cleanup)))
		}
		stopListening()
		for range events {
		}
	}()
	if err := (proto.FetchEnable{Patterns: []*proto.FetchRequestPattern{{
		URLPattern: "*", RequestStage: proto.FetchRequestStageResponse,
	}}}).Call(page); err != nil {
		return nil, err
	}

	triggerDone := make(chan error, 1)
	go func() { triggerDone <- trigger(page) }()
	triggerFinished := false
	defer func() {
		cancel()
		if !triggerFinished {
			err = errors.Join(err, <-triggerDone)
		}
	}()
	for {
		if cause := context.Cause(ctx); cause != nil {
			return result, cause
		}
		if result != nil && triggerFinished {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return result, context.Cause(ctx)
		case triggerErr := <-triggerDone:
			triggerFinished = true
			triggerDone = nil
			if triggerErr != nil {
				return result, triggerErr
			}
		case message, open := <-events:
			if !open {
				return result, errors.New("page event stream ended before response capture completed")
			}
			var paused proto.FetchRequestPaused
			if !message.Load(&paused) {
				continue
			}
			var readErr error
			if result == nil && paused.Request.URL == url && !redirectResponse(&paused) {
				result, readErr = readResponse(page, &paused)
			}
			if errors.Is(readErr, errIncompleteBody) {
				discardPage = true
				return nil, readErr
			}
			// Continue every pause, including nonmatches and failed reads. This
			// operation must survive the capture caller's cancellation.
			resumeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			resumeErr := (proto.FetchContinueRequest{RequestID: paused.RequestID}).Call(page.Context(resumeCtx))
			stop()
			if err := errors.Join(readErr, resumeErr); err != nil {
				return result, err
			}
		}
	}
}

func redirectResponse(paused *proto.FetchRequestPaused) bool {
	if paused.ResponseStatusCode == nil {
		return false
	}
	switch *paused.ResponseStatusCode {
	case 301, 302, 303, 307, 308:
		for _, header := range paused.ResponseHeaders {
			if strings.EqualFold(header.Name, "Location") {
				return true
			}
		}
	}
	return false
}

func readResponse(page *rod.Page, paused *proto.FetchRequestPaused) (*capturedResponse, error) {
	if paused.ResponseErrorReason != "" {
		return nil, fmt.Errorf("browser response failed: %s", paused.ResponseErrorReason)
	}
	if paused.ResponseStatusCode == nil {
		return nil, errors.New("request was not paused at the response stage")
	}
	response := &capturedResponse{URL: paused.Request.URL, Status: *paused.ResponseStatusCode}
	if paused.Request.Method == http.MethodHead || response.Status < 200 ||
		response.Status == 204 || response.Status == 205 || response.Status == 304 {
		return response, nil
	}
	// Let a finite body read complete before continuing or disabling Fetch,
	// even if the caller cancels meanwhile. CDP forbids racing those operations
	// with an outstanding getResponseBody. This buffered example limits reads
	// to five seconds; it is not a streaming-response reader.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(page.GetContext()), 5*time.Second)
	defer cancel()
	body, err := (proto.FetchGetResponseBody{RequestID: paused.RequestID}).Call(page.Context(ctx))
	if err != nil {
		if _, completed := errors.AsType[*cdp.Error](err); !completed {
			return nil, errors.Join(errIncompleteBody, err)
		}
		return nil, err
	}
	if body.Base64Encoded {
		response.Body, err = base64.StdEncoding.DecodeString(body.Body)
	} else {
		response.Body = []byte(body.Body)
	}
	return response, err
}
