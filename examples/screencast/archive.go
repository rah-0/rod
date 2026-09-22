package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"io"
	"math"
	"strings"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

const (
	frameLimit    = 3600
	maxFrameBytes = 4 << 20
)

var (
	errArchiveFinished = errors.New("screencast archive is finished")
	errNoFrames        = errors.New("screencast produced no frames")
	errFrameLimit      = errors.New("screencast frame limit exceeded")
	errInvalidFrame    = errors.New("invalid screencast frame")
	errInvalidTiming   = errors.New("invalid screencast timing")
	errInvalidOutput   = errors.New("screencast output writer is required")
)

type archivedFrame struct {
	name string
	at   time.Duration
}

// frameArchive has one owner. It streams image bytes and retains only the
// bounded manifest; the caller owns and closes the underlying output writer.
type frameArchive struct {
	writer         *tar.Writer
	started        time.Time
	lastReceived   time.Time
	frames         []archivedFrame
	clockAnchored  bool
	clockTimestamp float64
	clockReceived  time.Duration
	done           bool
	err            error
}

func newFrameArchive(output io.Writer, started time.Time) *frameArchive {
	a := &frameArchive{writer: tar.NewWriter(output), started: started}
	if output == nil {
		a.err = errInvalidOutput
	} else if started.IsZero() {
		a.err = errInvalidTiming
	}
	return a
}

func (a *frameArchive) WriteFrame(frame *proto.PageScreencastFrame, received time.Time) error {
	if a.err != nil {
		return a.err
	}
	if a.done {
		return errArchiveFinished
	}
	if len(a.frames) == frameLimit {
		return a.fail(errFrameLimit)
	}
	if frame == nil || len(frame.Data) == 0 || len(frame.Data) > maxFrameBytes {
		return a.fail(fmt.Errorf("%w: require a PNG of at most %d bytes", errInvalidFrame, maxFrameBytes))
	}
	// Validate the PNG header without allocating a decoded image.
	if _, err := png.DecodeConfig(bytes.NewReader(frame.Data)); err != nil {
		return a.fail(fmt.Errorf("%w: PNG header: %v", errInvalidFrame, err))
	}
	arrival, err := a.offset(received)
	if err != nil || received.Before(a.lastReceived) {
		return a.fail(errInvalidTiming)
	}
	position := arrival
	if frame.Metadata != nil && frame.Metadata.Timestamp != 0 {
		stamp := float64(frame.Metadata.Timestamp)
		if math.IsNaN(stamp) || math.IsInf(stamp, 0) {
			return a.fail(errInvalidTiming)
		}
		if !a.clockAnchored {
			a.clockAnchored = true
			a.clockTimestamp = stamp
			a.clockReceived = arrival
		}
		// Only the difference between browser timestamps is meaningful locally.
		seconds := a.clockReceived.Seconds() + (stamp - a.clockTimestamp)
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return a.fail(errInvalidTiming)
		}
		if seconds <= 0 {
			position = 0
		} else if seconds < arrival.Seconds() {
			position = min(time.Duration(seconds*float64(time.Second)), arrival)
		}
	}
	if len(a.frames) == 0 {
		// Display the initial image during startup as well as after its receipt.
		position = 0
	} else {
		position = max(position, a.frames[len(a.frames)-1].at)
	}
	name := fmt.Sprintf("frame-%06d.png", len(a.frames)+1)
	if err := a.writeFile(name, frame.Data); err != nil {
		return a.fail(err)
	}
	a.frames = append(a.frames, archivedFrame{name: name, at: position})
	a.lastReceived = received
	return nil
}

func (a *frameArchive) Finish(stopped time.Time) error {
	if a.err != nil || a.done {
		return a.err
	}
	a.done = true
	if len(a.frames) == 0 {
		return a.fail(errNoFrames)
	}
	end, err := a.offset(stopped)
	if err != nil || end == 0 || stopped.Before(a.lastReceived) {
		return a.fail(errInvalidTiming)
	}
	var manifest strings.Builder
	manifest.WriteString("ffconcat version 1.0\n")
	for i, frame := range a.frames {
		until := end
		if i+1 < len(a.frames) {
			until = a.frames[i+1].at
		}
		fmt.Fprintf(&manifest, "file '%s'\noption framerate 1000\nduration %.9f\n", frame.name, (until - frame.at).Seconds())
	}
	// A final packet makes the last image's duration visible to the concat demuxer.
	fmt.Fprintf(&manifest, "file '%s'\noption framerate 1000\n", a.frames[len(a.frames)-1].name)
	if err := a.writeFile("recording.ffconcat", []byte(manifest.String())); err != nil {
		return a.fail(err)
	}
	return a.fail(a.writer.Close())
}

func (a *frameArchive) offset(at time.Time) (time.Duration, error) {
	if at.Before(a.started) {
		return 0, errInvalidTiming
	}
	elapsed := at.Sub(a.started)
	if !a.started.Add(elapsed).Equal(at) {
		return 0, errInvalidTiming
	}
	return elapsed, nil
}

func (a *frameArchive) writeFile(name string, data []byte) error {
	if err := a.writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return fmt.Errorf("write %s header: %w", name, err)
	}
	n, err := a.writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func (a *frameArchive) fail(err error) error {
	a.err = err
	return err
}
