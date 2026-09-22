# Record a screencast

Run from the repository root with an installed Chrome or Chromium:

```sh
go run ./examples/screencast -out /tmp/screencast.tar
```

The example records a local moving square for three seconds, including its
initial and final pauses. The archive contains PNG frames and
`recording.ffconcat`, which describes their elapsed presentation times. The
output path must be new. Cancellation or failure removes the incomplete archive.

To create an MP4, use an optional FFmpeg installation with the `libx264` encoder:

```sh
mkdir /tmp/screencast-frames
tar -xf /tmp/screencast.tar -C /tmp/screencast-frames
ffmpeg -nostdin -f concat -safe 0 -i /tmp/screencast-frames/recording.ffconcat \
  -fps_mode vfr -c:v libx264 -bf 0 -pix_fmt yuv420p /tmp/screencast.mp4
```

Recording itself uses only Rod and Go's standard library. The encoder runs
separately; the example does not install or start it. The
[concat durations](https://ffmpeg.org/ffmpeg-formats.html#concat) preserve pauses
instead of treating incoming events as equally spaced frames. Browser timestamps
are anchored to local receipt time, with receipt time as the fallback when a
timestamp is absent. The first image fills the startup interval and the last
image remains visible until stop. The manifest uses a millisecond time base;
`-bf 0` avoids reordered frames shortening MP4 duration across long pauses.

`record` is an example-local helper for a dedicated page. It subscribes through
`Page.EachEvent` before starting screencast capture and acknowledges each received
frame. It owns the output's `Close` call. Closing its stop channel finishes the
archive; canceling the page context aborts it, including on static pages. Do not
run another screencast on that page concurrently.

The queue holds at most eight frames. If the writer falls behind, recording fails
instead of growing an unbounded queue. Capture is limited to one minute, 3,600
frames, and 4 MiB per frame, at a maximum resolution of 640 × 360. Output writers
must allow concurrent `Close` to unblock `Write`, as `io.PipeWriter` does. The
command uses a regular local file; a filesystem with indefinitely blocked I/O
cannot provide the same cancellation guarantee.

Start, acknowledgement, stop, and output finalization have five-second budgets.
Cleanup joins the event listener and closes the output. If stopping capture
fails, it also attempts to close the dedicated target; the command closes its
owning browser on every exit. Use an archive only when `record` returns nil;
discard output on error, even if its manifest was already written.

This captures viewport images without audio or browser chrome. Chrome can omit
frames under load; timestamps preserve elapsed time but cannot recover missing
images. It is a debugging example, not a fixed-frame-rate screen recorder.

Run the tests with:

```sh
go test -count=1 -race -cover -covermode=atomic ./examples/screencast
```

The video conversion check runs when `ffmpeg` and `ffprobe` are available. The
remaining checks need only the installed browser.
