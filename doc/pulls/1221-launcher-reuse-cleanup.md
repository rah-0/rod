# Let launcher cleanup finish after reusing an existing browser

**Priority:** P1 — shutdown can hang indefinitely.

**Source:** [upstream PR #1221](https://github.com/go-rod/rod/pull/1221).

## Why this still applies

[Launcher.Launch](../../lib/launcher/launcher.go) returns successfully when discovery finds a browser on the requested port. It starts no process and leaves `l.exit` open. `Launcher.Cleanup` nevertheless waits unconditionally on that channel. `Kill` returns immediately when the launcher's PID is zero, so calling it first does not unblock cleanup.

A reproduction served a valid `/json/version` response with `httptest`, then called `New().Bin("/bin/true").RemoteDebuggingPort(port).Launch()`. Launch returned a URL, no error, and PID zero; cleanup remained blocked after 200 ms. This requires no browser process.

## Task

Complete the launcher's exit bookkeeping when it successfully reuses a browser without owning a process. Preserve process ownership: cleanup must not kill the reused browser or remove a caller-supplied profile. Keep the normal launched-process path waiting for `cmd.Wait` before removing its owned temporary profile.

The original channel-close fix is a useful starting point. Apply it to this fork's direct-launch implementation; the upstream `Leakless` branch no longer exists. A separate public attach API is unnecessary for this correction.

## Acceptance criteria

- A fake discovery endpoint reproduces successful reuse, and subsequent cleanup returns promptly.
- `Kill` followed by cleanup also returns without affecting the reused endpoint or a supplied profile sentinel file.
- Repeated cleanup is safe; no path closes the exit channel twice.
- Existing cleanup and process-lifecycle tests still pass, including preservation of caller-owned profiles.
