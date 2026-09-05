# Add a reliable screencast recording example

Priority: P3 — useful optional example; preserve the dependency-free core.

Sources: [PR #1152](https://github.com/go-rod/rod/pull/1152), [PR #643](https://github.com/go-rod/rod/pull/643), [PR #614](https://github.com/go-rod/rod/pull/614).

## Why retain this task

Recording an automation run helps diagnose failures that a final screenshot cannot explain. The fork exposes `PageStartScreencast`, `PageScreencastFrame`, `PageScreencastFrameAck`, and `PageStopScreencast` in [the protocol package](../../lib/proto/page.go), but has no recording example that combines them with reliable cleanup.

The historical recording PRs were closed when upstream renamed `master` to `main`; the maintainer had welcomed continued work. Their feature remains useful, but the patches need redesign. PR #1152 also combines recording with a larger monitor change that is unnecessary for this task.

## Scope

- Add a self-contained example under `lib/examples` using the existing protocol calls and `Page.EachEvent`.
- Subscribe before starting capture, acknowledge received frames, and define bounded buffering or backpressure for a slow output writer.
- Give the recording one owner. Cancellation, page closure, protocol failures, and output failures must stop capture, release the event listener, and finish or abort the output predictably. Cleanup must also work while the page is static and no further frames arrive.
- Preserve elapsed time when constructing a video; screencast events do not imply a constant frame rate.
- If encoding uses an external `ffmpeg` executable, document that prerequisite and its explicit invocation in the example. Keep it optional and avoid adding encoder dependencies or installation steps to the core library.
- Explain the example's invocation, output, and limitations in a nearby README. Do not introduce new public `Page` methods or redesign the monitor for this task.

## Acceptance checks

- Record a local fixture with a moving element and verify that decoded frames show the movement.
- Include a static interval and verify that output timing follows elapsed recording time.
- Exercise normal stop, cancellation with no incoming frames, a slow/failing output writer, page closure, and start/ack failures. No goroutine, listener, subprocess, or open output file may remain owned by the example after it returns.
- Run the focused tests with the race detector; use local fixtures rather than live websites.
