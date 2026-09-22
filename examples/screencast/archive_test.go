package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/rah-0/rod/lib/proto"
)

type archiveTestEntry struct {
	name string
	data []byte
}

func archiveTestPNG(t *testing.T, value uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: value, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func readArchiveTestEntries(t *testing.T, data []byte) []archiveTestEntry {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(data))
	var entries []archiveTestEntry
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg {
			t.Fatalf("archive contains a non-file entry: %+v", header)
		}
		entries = append(entries, archiveTestEntry{name: header.Name, data: body})
	}
}

type archiveCloseBuffer struct {
	bytes.Buffer
	closed bool
}

func (output *archiveCloseBuffer) Close() error {
	output.closed = true
	return nil
}

func TestFrameArchiveTiming(t *testing.T) {
	started := time.Unix(1700000000, 0)
	first, second := archiveTestPNG(t, 50), archiveTestPNG(t, 200)
	var output archiveCloseBuffer
	archive := newFrameArchive(&output, started)
	if err := archive.WriteFrame(&proto.PageScreencastFrame{
		Data: first, Metadata: &proto.PageScreencastFrameMetadata{Timestamp: 1000},
	}, started.Add(250*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if output.Len() <= 512 {
		t.Fatal("frame bytes were buffered instead of written")
	}
	if err := archive.WriteFrame(&proto.PageScreencastFrame{
		Data: second, Metadata: &proto.PageScreencastFrameMetadata{Timestamp: 1000.5},
	}, started.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Finish(started.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	entries := readArchiveTestEntries(t, output.Bytes())
	if len(entries) != 3 || entries[0].name != "frame-000001.png" || entries[1].name != "frame-000002.png" || entries[2].name != "recording.ffconcat" {
		t.Fatalf("archive entries = %+v", entries)
	}
	if !bytes.Equal(entries[0].data, first) || !bytes.Equal(entries[1].data, second) {
		t.Fatal("archive changed PNG bytes")
	}
	want := "ffconcat version 1.0\nfile 'frame-000001.png'\noption framerate 1000\nduration 0.750000000\nfile 'frame-000002.png'\noption framerate 1000\nduration 3.250000000\nfile 'frame-000002.png'\noption framerate 1000\n"
	if got := string(entries[2].data); got != want {
		t.Fatalf("manifest = %q, want %q", got, want)
	}
	if output.closed {
		t.Fatal("archive closed its caller's output writer")
	}
	size := output.Len()
	if err := archive.Finish(started.Add(5 * time.Second)); err != nil || output.Len() != size {
		t.Fatalf("repeated Finish changed output: %v", err)
	}
	if err := archive.WriteFrame(&proto.PageScreencastFrame{Data: first}, started.Add(5*time.Second)); !errors.Is(err, errArchiveFinished) {
		t.Fatalf("write after Finish = %v", err)
	}
	if size < 1024 || !bytes.Equal(output.Bytes()[size-1024:], make([]byte, 1024)) {
		t.Fatal("successful archive has no TAR end marker")
	}
}

func TestFrameArchiveMissingAndClampedTimestamps(t *testing.T) {
	started := time.Unix(1700000000, 0)
	data := archiveTestPNG(t, 100)
	var output bytes.Buffer
	archive := newFrameArchive(&output, started)
	for i, metadata := range []*proto.PageScreencastFrameMetadata{
		nil, {}, {Timestamp: 900}, {Timestamp: 899}, {Timestamp: 1000}, nil,
	} {
		if err := archive.WriteFrame(&proto.PageScreencastFrame{Data: data, Metadata: metadata}, started.Add(time.Duration(i+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Finish(started.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	entries := readArchiveTestEntries(t, output.Bytes())
	manifest := string(entries[len(entries)-1].data)
	var durations []string
	for line := range strings.SplitSeq(manifest, "\n") {
		if strings.HasPrefix(line, "duration ") {
			durations = append(durations, strings.TrimPrefix(line, "duration "))
		}
	}
	if got, want := strings.Join(durations, ","), "2.000000000,1.000000000,0.000000000,2.000000000,1.000000000,4.000000000"; got != want {
		t.Fatalf("clamped frame durations = %s, want %s", got, want)
	}
}

func TestFrameArchiveClockOffset(t *testing.T) {
	started := time.Unix(1700000000, 0)
	data := archiveTestPNG(t, 100)
	for _, stamp := range []proto.TimeSinceEpoch{1000, 1e12, 1e30} {
		var output bytes.Buffer
		archive := newFrameArchive(&output, started)
		frame := &proto.PageScreencastFrame{Data: data, Metadata: &proto.PageScreencastFrameMetadata{Timestamp: stamp}}
		for _, received := range []time.Duration{250 * time.Millisecond, time.Second} {
			if err := archive.WriteFrame(frame, started.Add(received)); err != nil {
				t.Fatal(err)
			}
		}
		if err := archive.Finish(started.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		entries := readArchiveTestEntries(t, output.Bytes())
		manifest := string(entries[2].data)
		if !strings.Contains(manifest, "duration 0.250000000\n") || !strings.Contains(manifest, "duration 0.750000000\n") {
			t.Fatalf("remote timestamp offset %g changed local timing: %s", stamp, manifest)
		}
	}
}

func TestFrameArchiveStaticDuration(t *testing.T) {
	started := time.Unix(1700000000, 0)
	var output bytes.Buffer
	archive := newFrameArchive(&output, started)
	if err := archive.WriteFrame(&proto.PageScreencastFrame{Data: archiveTestPNG(t, 100)}, started.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Finish(started.Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	entries := readArchiveTestEntries(t, output.Bytes())
	manifest := string(entries[1].data)
	if !strings.Contains(manifest, "duration 8.000000000\n") || strings.Count(manifest, "file 'frame-000001.png'") != 2 {
		t.Fatalf("static interval was not preserved: %q", manifest)
	}
}

func TestFrameArchiveLimits(t *testing.T) {
	started := time.Unix(1700000000, 0)
	data := archiveTestPNG(t, 100)
	archive := newFrameArchive(io.Discard, started)
	for i := range frameLimit {
		if err := archive.WriteFrame(&proto.PageScreencastFrame{Data: data}, started.Add(time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatalf("frame %d: %v", i+1, err)
		}
	}
	if err := archive.WriteFrame(&proto.PageScreencastFrame{Data: data}, started.Add(4*time.Second)); !errors.Is(err, errFrameLimit) {
		t.Fatalf("frame limit = %v", err)
	}
	if err := archive.Finish(started.Add(5 * time.Second)); !errors.Is(err, errFrameLimit) {
		t.Fatalf("Finish lost frame-limit failure: %v", err)
	}
	large := make([]byte, maxFrameBytes)
	copy(large, data)
	archive = newFrameArchive(io.Discard, started)
	if err := archive.WriteFrame(&proto.PageScreencastFrame{Data: large}, started); err != nil {
		t.Fatalf("frame at size limit = %v", err)
	}
	if err := archive.WriteFrame(&proto.PageScreencastFrame{Data: append(large, 0)}, started); !errors.Is(err, errInvalidFrame) {
		t.Fatalf("oversized frame = %v", err)
	}
}

type invalidArchiveCase struct {
	name     string
	frame    *proto.PageScreencastFrame
	received time.Time
	want     error
}

func TestFrameArchiveInvalidInput(t *testing.T) {
	started := time.Unix(1700000000, 0)
	data := archiveTestPNG(t, 100)
	for _, test := range []invalidArchiveCase{
		{name: "nil frame", received: started, want: errInvalidFrame},
		{name: "empty frame", frame: &proto.PageScreencastFrame{}, received: started, want: errInvalidFrame},
		{name: "invalid PNG", frame: &proto.PageScreencastFrame{Data: []byte("invalid")}, received: started, want: errInvalidFrame},
		{name: "before start", frame: &proto.PageScreencastFrame{Data: data}, received: started.Add(-time.Second), want: errInvalidTiming},
		{name: "nan timestamp", frame: &proto.PageScreencastFrame{Data: data, Metadata: &proto.PageScreencastFrameMetadata{Timestamp: proto.TimeSinceEpoch(math.NaN())}}, received: started, want: errInvalidTiming},
		{name: "infinite timestamp", frame: &proto.PageScreencastFrame{Data: data, Metadata: &proto.PageScreencastFrameMetadata{Timestamp: proto.TimeSinceEpoch(math.Inf(1))}}, received: started, want: errInvalidTiming},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			archive := newFrameArchive(&output, started)
			if err := archive.WriteFrame(test.frame, test.received); !errors.Is(err, test.want) {
				t.Fatalf("WriteFrame = %v, want %v", err, test.want)
			}
			if err := archive.Finish(started.Add(time.Second)); !errors.Is(err, test.want) || output.Len() != 0 {
				t.Fatalf("invalid input finalized an archive: error=%v bytes=%d", err, output.Len())
			}
		})
	}
	if err := newFrameArchive(io.Discard, started).Finish(started.Add(time.Second)); !errors.Is(err, errNoFrames) {
		t.Fatalf("empty archive = %v", err)
	}
	if err := newFrameArchive(nil, started).Finish(started.Add(time.Second)); !errors.Is(err, errInvalidOutput) {
		t.Fatalf("nil output = %v", err)
	}
	if err := newFrameArchive(io.Discard, time.Time{}).Finish(started); !errors.Is(err, errInvalidTiming) {
		t.Fatalf("zero start = %v", err)
	}
	for _, phase := range []string{"backward receipt", "early stop", "zero duration"} {
		t.Run(phase, func(t *testing.T) {
			archive := newFrameArchive(io.Discard, started)
			received := started.Add(time.Second)
			if phase == "zero duration" {
				received = started
			}
			if err := archive.WriteFrame(&proto.PageScreencastFrame{Data: data}, received); err != nil {
				t.Fatal(err)
			}
			var err error
			if phase == "backward receipt" {
				err = archive.WriteFrame(&proto.PageScreencastFrame{Data: data}, started)
			} else {
				err = archive.Finish(started)
			}
			if !errors.Is(err, errInvalidTiming) {
				t.Fatalf("invalid timing = %v", err)
			}
		})
	}
}

type archiveFailWriter struct {
	failure    error
	remaining  int
	failOnTail bool
}

func (output *archiveFailWriter) Write(data []byte) (int, error) {
	if output.failOnTail {
		if len(data) == 512 && bytes.Equal(data, make([]byte, 512)) {
			return 0, output.failure
		}
	} else {
		if len(data) > output.remaining {
			return 0, output.failure
		}
		output.remaining -= len(data)
	}
	return len(data), nil
}

func TestFrameArchiveOutputFailures(t *testing.T) {
	started := time.Unix(1700000000, 0)
	data := archiveTestPNG(t, 100)
	failed := errors.New("output failed")
	for _, phase := range []string{"image header", "image data", "manifest", "tar close"} {
		t.Run(phase, func(t *testing.T) {
			output := &archiveFailWriter{failure: failed}
			switch phase {
			case "image data":
				output.remaining = 512
			case "manifest":
				output.remaining = 512 + len(data)
			case "tar close":
				output.failOnTail = true
			}
			archive := newFrameArchive(output, started)
			err := archive.WriteFrame(&proto.PageScreencastFrame{Data: data}, started)
			if phase == "manifest" || phase == "tar close" {
				if err != nil {
					t.Fatal(err)
				}
				err = archive.Finish(started.Add(time.Second))
			}
			if !errors.Is(err, failed) {
				t.Fatalf("output failure = %v", err)
			}
			if err := archive.Finish(started.Add(time.Second)); !errors.Is(err, failed) {
				t.Fatalf("Finish lost output failure: %v", err)
			}
			if err := archive.WriteFrame(&proto.PageScreencastFrame{Data: data}, started); !errors.Is(err, failed) {
				t.Fatalf("WriteFrame lost output failure: %v", err)
			}
		})
	}
}
