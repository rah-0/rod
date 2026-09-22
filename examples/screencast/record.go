package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/proto"
)

var (
	errListenerStopped = errors.New("screencast listener stopped")
	errSlowOutput      = errors.New("screencast output cannot keep up: frame queue is full")
)

type capturedFrame struct {
	frame    *proto.PageScreencastFrame
	received time.Time
}

type recordingStop struct {
	mu sync.Mutex
	at time.Time
}

// record exclusively owns screencasting on a dedicated page and closes output.
// Closing stop finishes the archive. Cancellation or any failure aborts it.
// Output must permit concurrent Close and Write, with Close unblocking Write;
// io.PipeWriter satisfies this contract. Use ordinary local files for disk output.
func record(page *rod.Page, stop <-chan struct{}, output io.WriteCloser) (err error) {
	limit, stopLimit := context.WithTimeout(page.GetContext(), time.Minute)
	defer stopLimit()
	ctx, abort := context.WithCancelCause(limit)
	defer abort(nil)
	defer func() {
		if cause := context.Cause(ctx); cause != nil && !errors.Is(err, cause) {
			err = errors.Join(err, cause)
		}
	}()
	var closeOnce sync.Once
	var closeErr error
	closeOutput := func() { closeOnce.Do(func() { closeErr = output.Close() }) }
	interruptOutput := context.AfterFunc(ctx, closeOutput)
	defer func() {
		interruptOutput()
		closeOutput() // Also joins an already-running cancellation callback.
		err = errors.Join(err, closeErr)
	}()
	if err := context.Cause(ctx); err != nil {
		return err
	}

	listenCtx, stopListening := context.WithCancelCause(ctx)
	frames := make(chan capturedFrame, 8)
	wait := page.Context(listenCtx).EachEvent(rod.On(func(frame *proto.PageScreencastFrame, _ proto.TargetSessionID) bool {
		if listenCtx.Err() != nil {
			return true
		}
		var queueErr error
		select {
		case frames <- capturedFrame{frame: frame, received: time.Now()}:
		default:
			queueErr = errSlowOutput
		}
		// Acknowledge even the frame that overflows the queue, then abort. A
		// blocking output writer never holds up event processing or cleanup.
		ackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ackErr := (proto.PageScreencastFrameAck{SessionID: frame.SessionID}).Call(page.Context(ackCtx))
		cancel()
		if failure := errors.Join(queueErr, listenerError(ackErr)); failure != nil {
			abort(failure)
			return true
		}
		return listenCtx.Err() != nil
	}))
	listenerDone := make(chan error, 1)
	go func() {
		failure := listenerError(wait())
		if failure != nil {
			abort(failure)
		} else if listenCtx.Err() == nil {
			abort(errors.New("screencast listener ended unexpectedly"))
		}
		listenerDone <- failure
	}()

	// Join the listener before finalizing output. In particular, static pages
	// need cancellation to release the subscription without another frame.
	var shutdownOnce sync.Once
	var shutdownErr error
	shutdown := func() {
		shutdownOnce.Do(func() {
			stopListening(errListenerStopped)
			shutdownErr = <-listenerDone
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			stopErr := (proto.PageStopScreencast{}).Call(page.Context(cleanup))
			cancel()
			if stopErr != nil {
				// If stop cannot be confirmed, discard the dedicated target.
				cleanup, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				closed, closeErr := (proto.TargetCloseTarget{TargetID: page.TargetID}).Call(page.Browser().Context(cleanup))
				cancel()
				if closeErr == nil && !closed.Success {
					closeErr = errors.New("browser did not close the screencast page")
				}
				shutdownErr = errors.Join(shutdownErr, stopErr, closeErr)
			}
		})
	}
	defer func() {
		shutdown()
		err = errors.Join(err, shutdownErr)
	}()

	// Observe stop independently of disk writes. Finalization gets five seconds
	// before a stalled output is closed and the archive is reported incomplete.
	stopRequested := make(chan time.Time, 1)
	var cutoff recordingStop
	watchDone := make(chan struct{})
	finishWatch := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			return
		case <-finishWatch:
			return
		case <-stop:
			cutoff.mu.Lock()
			cutoff.at = time.Now()
			stopped := cutoff.at
			cutoff.mu.Unlock()
			stopRequested <- stopped
			stopListening(errListenerStopped)
		}
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-finishWatch:
		case <-timer.C:
			abort(errors.New("screencast output did not finish within five seconds of stop"))
		}
	}()
	defer func() { close(finishWatch); <-watchDone }()

	started := time.Now()
	archive := newFrameArchive(output, started)
	writeFrame := func(frame capturedFrame) error {
		cutoff.mu.Lock()
		include := cutoff.at.IsZero() || !frame.received.After(cutoff.at)
		cutoff.mu.Unlock()
		if include {
			return archive.WriteFrame(frame.frame, frame.received)
		}
		return nil
	}
	startCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = (proto.PageStartScreencast{
		Format:   proto.PageStartScreencastFormatPng,
		MaxWidth: new(640), MaxHeight: new(360), EveryNthFrame: new(1),
	}).Call(page.Context(startCtx))
	cancel()
	if err != nil {
		return fmt.Errorf("start screencast: %w", err)
	}
	for {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case stopped := <-stopRequested:
			shutdown()
			if failure := errors.Join(context.Cause(ctx), shutdownErr); failure != nil {
				return failure
			}
			// The producer is joined, so the remaining queue is finite. Frames
			// received after stop do not extend the recording's elapsed time.
			for len(frames) != 0 {
				if err := writeFrame(<-frames); err != nil {
					return err
				}
			}
			if err := archive.Finish(stopped); err != nil {
				return err
			}
			return context.Cause(ctx)
		case frame := <-frames:
			if err := writeFrame(frame); err != nil {
				return err
			}
		}
	}
}

// EachEvent can join its termination cause with a domain restoration error.
// Remove only our own stop marker, preserving any real cleanup failure.
func listenerError(err error) error {
	if err == nil || err == errListenerStopped {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var result error
		for _, child := range joined.Unwrap() {
			result = errors.Join(result, listenerError(child))
		}
		return result
	}
	return err
}
