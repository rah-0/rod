package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "screencast.tar")
	var output bytes.Buffer
	if err := run(t.Context(), &output, path); err != nil {
		t.Fatal(err)
	}
	if want := "Recorded: " + path + "\n"; output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	archive, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	reader := tar.NewReader(archive)
	var manifest []byte
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(header.Name) != header.Name {
			t.Fatalf("unexpected archive path: %s", header.Name)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, header.Name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if header.Name == "recording.ffconcat" {
			manifest = data
		}
	}
	var duration, finalHold float64
	for line := range strings.SplitSeq(string(manifest), "\n") {
		if value, ok := strings.CutPrefix(line, "duration "); ok {
			finalHold, err = strconv.ParseFloat(value, 64)
			if err != nil {
				t.Fatal(err)
			}
			duration += finalHold
		}
	}
	if math.Abs(duration-3) > 0.3 || finalHold < 0.8 {
		t.Fatalf("recording timing: duration=%f final static hold=%f", duration, finalHold)
	}
	t.Run("video", func(t *testing.T) {
		verifyVideo(t, directory, duration)
	})
}

func TestRunOutputLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.tar")
	if err := os.WriteFile(path, []byte("existing recording"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), io.Discard, path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing output: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "existing recording" {
		t.Fatalf("existing output changed: %q, %v", data, err)
	}

	path = filepath.Join(t.TempDir(), "canceled.tar")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, io.Discard, path) }()
	finished := false
	defer func() {
		cancel()
		if !finished {
			<-done
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			finished = true
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled output: %v", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete archive was retained: %v", err)
			}
			return
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				cancel()
			}
		}
	}
}

func verifyVideo(t *testing.T, directory string, duration float64) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("optional MP4 verification requires ffmpeg with libx264")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("optional MP4 verification requires ffprobe")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	video := filepath.Join(directory, "recording.mp4")
	command := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-nostdin",
		"-f", "concat", "-safe", "0", "-i", filepath.Join(directory, "recording.ffconcat"),
		"-fps_mode", "vfr", "-c:v", "libx264", "-bf", "0", "-pix_fmt", "yuv420p", video)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("encode: %v\n%s", err, output)
	}
	command = exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", video)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	encodedDuration, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || math.Abs(encodedDuration-duration) > 0.05 {
		t.Fatalf("encoded duration=%q, recorded=%f: %v", output, duration, err)
	}
	// Sample the decoded video, including the held final image. A constant-rate
	// input interpretation would shorten the static interval and fail duration.
	command = exec.CommandContext(ctx, ffmpeg, "-v", "error", "-nostdin", "-i", video,
		"-vf", "fps=2,scale=160:90", "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
	output, err = command.Output()
	if err != nil {
		t.Fatal(err)
	}
	const frameBytes = 160 * 90 * 3
	if len(output)%frameBytes != 0 || len(output)/frameBytes < 5 {
		t.Fatalf("decoded %d bytes, want at least five complete frames", len(output))
	}
	first := redCenter(output[:frameBytes])
	last := redCenter(output[len(output)-frameBytes:])
	previous := redCenter(output[len(output)-2*frameBytes : len(output)-frameBytes])
	if first < 0 || last-first < 80 || math.Abs(last-previous) > 1 {
		t.Fatalf("decoded square positions: first=%f previous=%f last=%f", first, previous, last)
	}
}

func redCenter(frame []byte) float64 {
	var total, count int
	for pixel := range len(frame) / 3 {
		r, g, b := frame[pixel*3], frame[pixel*3+1], frame[pixel*3+2]
		if r > 180 && g < 80 && b < 80 {
			total += pixel % 160
			count++
		}
	}
	if count == 0 {
		return -1
	}
	return float64(total) / float64(count)
}
