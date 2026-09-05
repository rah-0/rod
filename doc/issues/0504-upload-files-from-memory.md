# Allow file inputs to receive files from memory

Priority: P2 · Bounded enhancement · Confirmed API gap by static inspection on 2026-09-05.

Sources: [upstream issue #504](https://github.com/go-rod/rod/issues/504), [#645](https://github.com/go-rod/rod/issues/645), [#1030](https://github.com/go-rod/rod/issues/1030).

[Element.SetFiles](../../element.go) converts client-side paths to absolute paths and sends `DOM.setFileInputFiles`. The browser interprets those paths on its own host. A file that exists only on the Go client's machine is therefore unavailable to a remotely managed browser. Callers with bytes in memory also need an otherwise unnecessary on-disk file.

Add an additive file-input helper that accepts a filename, MIME type and bytes for each file. Implement transfer through the page's existing JavaScript/CDP channel, constructing `File` objects and assigning a `DataTransfer` file list in the target input's execution context. Retain the existing path-based API for browser-host files. Writing a temporary file only on the client would not solve remote upload.

Keep this within the dependency-free core and existing manager transport. No separate upload server or changes to manager filesystem permissions are needed. Preserve the distinction between synthetic input/change events and trusted browser input.

Acceptance criteria:

- Local and remotely connected browser fixtures submit exactly the supplied names, MIME types and bytes.
- Cover multiple files, an empty file, Unicode filenames and non-UTF-8 bytes.
- Validate file-input targets and define the behavior of an empty list and a non-multiple input.
- Cancellation returns through the existing error style and does not leave temporary browser resources behind.
- Existing `SetFiles` behavior remains intact; document browser-host paths versus in-memory transfer and cover the Must variant if added.
