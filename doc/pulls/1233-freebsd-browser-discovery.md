# Discover installed browsers on FreeBSD

**Priority:** P2 — small platform-support gap.

**Source:** [upstream PR #1233](https://github.com/go-rod/rod/pull/1233).

## Why this still applies

[launcher.LookPath](../../lib/launcher/browser.go) selects executable candidates by `runtime.GOOS`. It has an OpenBSD entry but no FreeBSD entry, so FreeBSD searches an empty list even when `chrome` or `chromium` is available through `PATH`. Callers can currently work around this with an explicit `Launcher.Bin` path.

The fork intentionally relies on installed browsers, making this discovery improvement relevant. The upstream change to `Browser.BinPath` is obsolete here: browser downloading and revision selection have been removed.

## Task

Add FreeBSD executable candidates to `LookPath`, using the existing discovery pattern and `exec.LookPath`. Retain only the installed-browser portion of the upstream PR. Document the supported discovery behavior in the [launcher README](../../lib/launcher/README.md).

## Acceptance criteria

- On FreeBSD, an executable named `chrome` or `chromium` in a temporary `PATH` is discovered.
- With neither candidate available, discovery reports no result and normal launch returns `ErrBrowserNotFound`.
- An explicit `Launcher.Bin` continues to take precedence.
- Cross-compile the launcher package for FreeBSD; run discovery checks on FreeBSD before claiming runtime validation.
