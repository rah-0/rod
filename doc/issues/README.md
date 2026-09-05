# Issue backlog

Selected tasks from [upstream Rod issues](https://github.com/go-rod/rod/issues), grouped by implementation priority. Each task includes source reports, evidence, scope, and acceptance criteria.

There are **28 tasks**: 11 P1, 13 P2, 4 P3.

## P1 — High priority

Crashes, hangs, and failures involving data, cancellation, or resource ownership.

- [Avoid helper-cache writes after execution-context reset](0707-helper-cache-reset.md)
- [Keep shared event domains enabled until their last listener stops](0737-shared-event-domain-lifetime.md)
- [Make download waits reliable across new tabs and cancellation](0916-download-cancellation-error.md)
- [Generate valid WebSocket handshake keys and honor header overrides](1092-websocket-handshake-nonce.md)
- [Propagate cloned page contexts to input devices](1156-input-device-context.md)
- [Respect the caller's context in WaitRepaint](1179-respect-context-in-wait-repaint.md)
- [Preserve explicit false for optional CDP boolean parameters](1196-preserve-explicit-false-protocol-fields.md)
- [Separate cached target sessions from caller page contexts](1206-cached-page-context.md)
- [Encode numeric keypad input as digits instead of navigation keys](1212-numpad-key-encoding.md)
- [Release CDP state retained after a page closes](1226-release-closed-page-states.md)
- [Handle cross-process iframe sessions explicitly](1234-cross-process-iframes.md) — safe failure first; complete iframe support is P2.

## P2 — Medium priority

Correctness, compatibility, and focused API improvements.

- [Preserve available binary data in intercepted request bodies](0061-preserve-binary-hijack-request-bodies.md)
- [Restrict incognito page enumeration to its browser context](0437-incognito-pages-context.md)
- [Allow file inputs to receive files from memory](0504-upload-files-from-memory.md)
- [Discover installed Chromium browsers on FreeBSD](0828-freebsd-browser-discovery.md)
- [Encode macOS editing commands using the full key combination](0832-macos-editing-shortcuts.md)
- [Accept an empty text regex in ElementR](0958-empty-element-regex.md)
- [Translate CDP hijack globs without regexp panics or unintended matches](0982-hijack-glob-patterns.md)
- [Keep the document-load wait alive across navigation context replacement](1157-wait-load-across-navigation.md)
- [Make SetBlockedURLs take effect on a fresh page](1168-enable-network-for-blocked-urls.md)
- [Reconcile NewUserMode with Chrome's default-profile debugging restriction](1189-document-chrome-user-profile-restriction.md)
- [Crop element screenshots in screenshot pixel coordinates](1198-element-screenshot-scale.md)
- [Honor page contexts in browser-routed page methods](1206-page-method-context.md)
- [Preserve experimental tab metadata in TargetInfo](1239-target-tab-metadata.md)

## P3 — Low priority

Optional features, examples, and documentation.

- [Document query selection and waiting behavior together](0159-query-selection-guide.md)
- [Add a bounded helper for native HTML drag and drop](0392-native-drag-drop.md)
- [Wait for document parsing without waiting for every subresource](0494-wait-until-document-interactive.md)
- [Add an example for capturing browser-owned responses](0607-browser-response-capture.md)
