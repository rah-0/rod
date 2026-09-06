# Let launcher cleanup finish after reusing an existing browser

**Priority:** P1 — shutdown can hang indefinitely.

**Status:** Implemented. `TestCleanupReusedBrowser` covers discovery reuse,
repeated cleanup, endpoint survival, and caller-owned profile preservation.
`TestCleanupWithoutProcess` covers unused launchers and failed startup.

**Source:** [upstream PR #1221](https://github.com/go-rod/rod/pull/1221).

## Original failure

[Launcher.Launch](../../lib/launcher/launcher.go) returned successfully when discovery found a browser on the requested port, but left `l.exit` open without starting a process. `Launcher.Cleanup` waited unconditionally on that channel. `Kill` returned immediately when the launcher's PID was zero, so calling it first did not unblock cleanup. Launch now completes its exit bookkeeping on every path that starts no process.

A reproduction served a valid `/json/version` response with `httptest`, then called `New().Bin("/bin/true").RemoteDebuggingPort(port).Launch()`. Launch returned a URL, no error, and PID zero; cleanup remained blocked after 200 ms. This requires no browser process.

## Task

Complete the launcher's exit bookkeeping when it successfully reuses a browser without owning a process. Preserve process ownership: cleanup must not kill the reused browser or remove a caller-supplied profile. Keep the normal launched-process path waiting for `cmd.Wait` before removing its owned temporary profile.

The original channel-close fix is a useful starting point. Apply it to this fork's direct-launch implementation; the upstream `Leakless` branch no longer exists. A separate public attach API is unnecessary for this correction.

## Acceptance criteria

- A fake discovery endpoint reproduces successful reuse, and subsequent cleanup returns promptly.
- `Kill` followed by cleanup also returns without affecting the reused endpoint or a supplied profile sentinel file.
- Repeated cleanup is safe; no path closes the exit channel twice.
- Existing cleanup and process-lifecycle tests still pass, including preservation of caller-owned profiles.
