# Make download waits reliable across new tabs and cancellation

- Priority: P1
- Sources: [#916](https://github.com/go-rod/rod/issues/916), unresolved closed report [#971](https://github.com/go-rod/rod/issues/971).
- Reviewed: 2026-09-05.
- Validation: reproduced with Chrome 152; `browser.Timeout(50*time.Millisecond).MustWaitDownload()()` with no download panics with a nil-pointer error.

[Browser.WaitDownload](../../browser.go) returns a nil start event if its context expires before `Page.downloadWillBegin`. [Browser.MustWaitDownload](../../must.go) immediately reads `info.GUID`, so an ordinary timeout becomes a runtime nil-pointer panic rather than the configured Must error path. The existing cancellation test exercises only `WaitDownload`'s nil return.

The waiter also subscribes to deprecated `Page.downloadWillBegin` and `Page.downloadProgress` events. In [#971](https://github.com/go-rod/rod/issues/971#issuecomment-1791840295), the maintainer confirms that a download opened with `target="_blank"` can omit the Page start event. The current protocol already provides `Browser.downloadWillBegin` and `Browser.downloadProgress`.

Use the browser-wide download events and enable them through `Browser.setDownloadBehavior`. Check metadata before constructing the file path and route cancellation/deadline errors through the browser's configured error handler. Keep completion associated with the selected download GUID. Preserve public signatures, adapting the new event metadata to the existing return type where needed. No remote file-transfer service is required.

Acceptance criteria:

- A deadline before a download starts reports `context.DeadlineExceeded` through the Must error handler, never a nil dereference.
- Explicit cancellation and event-stream termination without a start event report meaningful errors.
- Same-tab and `target="_blank"` downloads complete from browser-wide events; unrelated GUIDs do not satisfy the wait.
- Successful downloads are still read and their temporary file removed; previous download behavior is restored.
- Cancellation after a start event is not mistaken for a completed file.
- Regression coverage includes `MustWaitDownload`, the custom panic handler, and the existing low-level nil-on-cancel contract.
