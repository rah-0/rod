# Discover installed Chromium browsers on FreeBSD

- Priority: P2
- Source: [#828](https://github.com/go-rod/rod/issues/828).
- Reviewed: 2026-09-05.
- Validation: static code confirmation; [LookPath](../../lib/launcher/browser.go) has no FreeBSD entry, so it always reports no browser there even when a suitable executable is on `PATH`.

Add FreeBSD browser names to local executable discovery, including the issue's `chrome` and `ungoogled-chromium` installations. Check the current FreeBSD package executable names during implementation and use `exec.LookPath` consistently with other platforms. The direct Unix launcher path is already shared through [os_unix.go](../../lib/launcher/os_unix.go). The launcher requires an installed browser, so the upstream request's installer changes are unnecessary.

Acceptance criteria:

- Discovery finds supported FreeBSD browser executables on `PATH` and still reports absence accurately.
- Explicit `Launcher.Bin` behavior and other operating-system search ordering remain intact.
- Cross-compilation verifies the FreeBSD launcher path; a FreeBSD host smoke test confirms launch and shutdown before claiming runtime support.
- Launcher documentation states the installed-browser requirement without adding a downloader or helper dependency.
